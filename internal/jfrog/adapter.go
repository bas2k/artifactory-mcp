// Package jfrog binds the read-only application adapter to jfrog-client-go.
package jfrog

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	app "artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/config"

	sdk "github.com/jfrog/jfrog-client-go/artifactory"
	"github.com/jfrog/jfrog-client-go/artifactory/auth"
	sdkconfig "github.com/jfrog/jfrog-client-go/config"
	"github.com/jfrog/jfrog-client-go/utils/log"
)

// Forward SDK diagnostics through slog, redacting configured access tokens.
type diagnosticLogger struct{}

var diagnosticTokens sync.Map

func (l diagnosticLogger) Verbose(a ...interface{}) { l.emit(slog.LevelDebug, a...) }
func (l diagnosticLogger) Debug(a ...interface{})   { l.emit(slog.LevelDebug, a...) }
func (l diagnosticLogger) Info(a ...interface{})    { l.emit(slog.LevelInfo, a...) }
func (l diagnosticLogger) Warn(a ...interface{})    { l.emit(slog.LevelWarn, a...) }
func (l diagnosticLogger) Error(a ...interface{})   { l.emit(slog.LevelError, a...) }
func (l diagnosticLogger) Output(a ...interface{})  { l.emit(slog.LevelError, a...) }

func (diagnosticLogger) emit(level slog.Level, a ...interface{}) {
	ctx := context.Background()
	if !slog.Default().Enabled(ctx, level) {
		return
	}
	message := strings.TrimSuffix(fmt.Sprintln(a...), "\n")
	diagnosticTokens.Range(func(token, _ any) bool {
		message = strings.ReplaceAll(message, token.(string), "[REDACTED]")
		return true
	})
	slog.Log(ctx, level, message)
}
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

func ConfigureLogging(tokens ...string) {
	for _, token := range append(tokens, os.Getenv("ARTIFACTORY_ACCESS_TOKEN")) {
		if token != "" {
			diagnosticTokens.Store(token, struct{}{})
		}
	}
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
	ConfigureLogging(c.Token)
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
	case "version", "repositories", "artifact", "folder", "properties", "stats", "builds", "build", "build_runs":
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
	case "version":
		if relative != "api/system/version" {
			return invalid
		}
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
	case "build_runs":
		name := strings.TrimPrefix(u.Path, "api/build/")
		if !strings.HasPrefix(u.Path, "api/build/") || config.ValidateSegment(name) != nil {
			return invalid
		}
		allowedKeys = []string{"project"}
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
		if !slices.Contains(allowedKeys, key) || len(values) != 1 {
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
