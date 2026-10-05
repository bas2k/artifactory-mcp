// Package jfrog binds the read-only application adapter to jfrog-client-go.
package jfrog

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	app "artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/config"

	sdk "github.com/jfrog/jfrog-client-go/artifactory"
	"github.com/jfrog/jfrog-client-go/artifactory/auth"
	sdkconfig "github.com/jfrog/jfrog-client-go/config"
	"github.com/jfrog/jfrog-client-go/utils/log"
)

// SDK diagnostics may embed credentials, query values, or upstream bodies.
// Forward only fixed events through the configured logger, including explicit
// SDK Output calls. Never format or forward the original arguments.
type diagnosticLogger struct{}

const diagnosticMessage = "JFrog SDK diagnostic; details suppressed"

func (diagnosticLogger) Debug(...interface{})  { slog.Debug(diagnosticMessage) }
func (diagnosticLogger) Info(...interface{})   { slog.Info(diagnosticMessage) }
func (diagnosticLogger) Warn(...interface{})   { slog.Warn(diagnosticMessage) }
func (diagnosticLogger) Error(...interface{})  { slog.Error(diagnosticMessage) }
func (diagnosticLogger) Output(...interface{}) { slog.Error(diagnosticMessage) }
func (diagnosticLogger) GetLogLevel() log.LevelType {
	for _, level := range []struct {
		slog slog.Level
		sdk  log.LevelType
	}{{slog.LevelDebug, log.DEBUG}, {slog.LevelInfo, log.INFO}, {slog.LevelWarn, log.WARN}} {
		if slog.Default().Enabled(context.Background(), level.slog) {
			return level.sdk
		}
	}
	return log.ERROR
}

var logging sync.Once

func ConfigureLogging() {
	logging.Do(func() {
		log.SetLogger(diagnosticLogger{})
	})
}

type executor struct {
	configuration  config.Config
	certificates   string
	managerFactory func(context.Context, config.Config, string) (sdk.ArtifactoryServicesManager, error)
}

func New(c config.Config) (*app.Client, error) { return newClient(c, newManager) }

func newClient(c config.Config, factory func(context.Context, config.Config, string) (sdk.ArtifactoryServicesManager, error)) (*app.Client, error) {
	ConfigureLogging()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	e := &executor{configuration: c, managerFactory: factory}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, app.NewError("invalid_input", "cannot read custom CA file")
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return nil, app.NewError("invalid_input", "custom CA file contains no certificates")
		}
		// The SDK accepts a certificate directory. Give it exactly the selected CA.
		directory, err := os.MkdirTemp("", "artifactory-mcp-ca-")
		if err != nil {
			return nil, app.NewError("unavailable", "cannot prepare custom CA")
		}
		if err := os.WriteFile(filepath.Join(directory, "custom.pem"), pem, 0600); err != nil {
			os.RemoveAll(directory)
			return nil, app.NewError("unavailable", "cannot prepare custom CA")
		}
		e.certificates = directory
	}
	return app.New(e, c.Repositories), nil
}
func (e *executor) Close() error {
	if e.certificates != "" {
		return os.RemoveAll(e.certificates)
	}
	return nil
}

func (e *executor) Execute(parent context.Context, operation, relative string, body []byte) ([]byte, error) {
	if err := validateOperation(operation, relative); err != nil {
		return nil, err
	}
	method := http.MethodGet
	switch operation {
	case "repositories", "artifact", "folder", "properties", "stats", "builds", "build":
	case "aql":
		method = http.MethodPost
	default:
		return nil, app.NewError("forbidden", "unsupported upstream operation")
	}
	ctx, cancel := context.WithTimeout(parent, e.configuration.Timeout)
	defer cancel()
	// Each SDK manager owns its context. No request mutates a shared SDK client.
	manager, err := e.managerFactory(ctx, e.configuration, e.certificates)
	if err != nil {
		return nil, app.Classify(err)
	}
	details := manager.GetConfig().GetServiceDetails()
	httpDetails := details.CreateHttpClientDetails()
	if method == http.MethodPost {
		httpDetails.Headers = map[string]string{"Content-Type": "text/plain"}
	}
	// Use the SDK's authenticated, context-aware client, leaving bodies open so
	// response limits apply before decoding. SDK DTO wrappers omit AQL notices,
	// started timestamps, and some exact values. No transport hooks are installed.
	resp, _, _, err := manager.Client().Send(method, details.GetUrl()+relative, body, false, false, &httpDetails, "")
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if ctx.Err() != nil {
		return nil, app.Classify(ctx.Err())
	}
	if resp != nil && resp.StatusCode != http.StatusOK {
		return nil, app.StatusError(resp.StatusCode)
	}
	if err != nil {
		return nil, app.Classify(err)
	}
	if resp == nil {
		return nil, app.NewError("unavailable", "upstream returned no response")
	}
	if resp.ContentLength > e.configuration.ResponseLimit {
		return nil, app.NewError("response_too_large", "upstream response exceeds the configured byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, e.configuration.ResponseLimit+1))
	if err != nil {
		return nil, app.Classify(err)
	}
	if int64(len(data)) > e.configuration.ResponseLimit {
		return nil, app.NewError("response_too_large", "upstream response exceeds the configured byte limit")
	}
	return app.RedactJSON(data, e.configuration.Token)
}

// Validate the operation before constructing its authenticated URL. This uses
// SDK request options and application validation, without a transport hook.
func validateOperation(operation, relative string) error {
	invalid := app.NewError("forbidden", "upstream operation is outside the read allowlist")
	u, err := url.Parse(relative)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || strings.HasPrefix(u.Path, "/") {
		return invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return invalid
	}
	allowedKeys := []string{}
	switch operation {
	case "repositories":
		if u.Path != "api/repositories" {
			return invalid
		}
		allowedKeys = []string{"type", "packageType", "project"}
	case "aql":
		if relative != "api/search/aql" {
			return invalid
		}
	case "builds":
		if u.Path != "api/build" {
			return invalid
		}
		allowedKeys = []string{"project"}
	case "build":
		parts := strings.Split(strings.TrimPrefix(u.Path, "api/build/"), "/")
		if !strings.HasPrefix(u.Path, "api/build/") || len(parts) != 2 || config.ValidateSegment(parts[0]) != nil || config.ValidateSegment(parts[1]) != nil {
			return invalid
		}
		allowedKeys = []string{"project", "started"}
	case "artifact", "folder", "properties", "stats":
		if !strings.HasPrefix(u.Path, "api/storage/") || config.ValidatePath(strings.TrimPrefix(u.Path, "api/storage/"), false) != nil {
			return invalid
		}
		if operation == "properties" {
			allowedKeys = []string{"properties"}
			if !q.Has("properties") {
				return invalid
			}
		}
		if operation == "stats" {
			allowedKeys = []string{"stats"}
			if !q.Has("stats") {
				return invalid
			}
		}
	default:
		return invalid
	}
	for key, values := range q {
		ok := false
		for _, allowed := range allowedKeys {
			if key == allowed {
				ok = true
			}
		}
		if !ok || len(values) != 1 {
			return invalid
		}
	}
	return nil
}

func newManager(ctx context.Context, c config.Config, certificates string) (sdk.ArtifactoryServicesManager, error) {
	details := auth.NewArtifactoryDetails()
	details.SetUrl(strings.TrimRight(c.URL, "/") + "/")
	details.SetAccessToken(c.Token)
	cfg, err := sdkconfig.NewConfigBuilder().SetServiceDetails(details).SetContext(ctx).SetCertificatesPath(certificates).SetInsecureTls(false).SetHttpRetries(0).SetOverallRequestTimeout(c.Timeout).Build()
	if err != nil {
		return nil, err
	}
	return sdk.New(cfg)
}
