package jfrog

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/search"
)

func settings(url string) config.Config {
	return config.Config{URL: url + "/proxy/artifactory", Token: "test-token", AllowHTTP: true, Timeout: time.Second, ResponseLimit: 5 << 20, Repositories: []string{"libs"}}
}
func TestHTTPContracts(t *testing.T) {
	var requests atomic.Int32
	server := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bearer authentication")
		}
		if !strings.HasPrefix(r.URL.Path, "/proxy/artifactory/api/") {
			t.Errorf("lost reverse proxy prefix: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && r.URL.Path == "/proxy/artifactory/api/search/aql") {
			t.Errorf("mutating request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/proxy/artifactory/api/repositories":
			if r.URL.Query().Get("type") != "local" || r.URL.Query().Get("packageType") != "generic" || r.URL.Query().Get("project") != "p" {
				t.Error("lost repository filters")
			}
			io.WriteString(w, `[{"key":"other","type":"LOCAL"},{"key":"libs","type":"LOCAL","packageType":"generic"}]`)
		case "/proxy/artifactory/api/search/aql":
			if r.Header.Get("Content-Type") != "text/plain" {
				t.Error("wrong AQL content type")
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"$or":[{"repo":"libs"}]`) || !strings.Contains(string(body), `.include("repo","path","name"`) || !strings.HasSuffix(string(body), `.offset(0).limit(1)`) {
				t.Errorf("invalid generated AQL: %s", body)
			}
			data, _ := os.ReadFile("../../testdata/search.json")
			w.Write(data)
		case "/proxy/artifactory/api/storage/libs/目录/a space;tag.jar":
			if !strings.Contains(r.RequestURI, "%20") || !strings.Contains(r.RequestURI, "%3B") {
				t.Errorf("unescaped path: %s", r.RequestURI)
			}
			if r.URL.Query().Has("properties") {
				io.WriteString(w, `{"properties":{"wanted":["value"],"credential":["test-token"]}}`)
			} else if r.URL.Query().Has("stats") {
				io.WriteString(w, `{"downloadCount":9007199254740993,"lastDownloaded":123}`)
			} else {
				io.WriteString(w, `{"repo":"libs","path":"/目录/a space;tag.jar","size":"9007199254740993","checksums":{"sha256":"abcdef"}}`)
			}
		case "/proxy/artifactory/api/storage/libs":
			io.WriteString(w, `{"repo":"libs","path":"/","children":[{"uri":"/a.jar","folder":false},{"uri":"/dir","folder":true}]}`)
		case "/proxy/artifactory/api/build":
			if r.URL.Query().Get("project") != "p" {
				t.Error("lost build project")
			}
			io.WriteString(w, `{"builds":[{"uri":"/z-job","lastStarted":"2026-01-01"},{"uri":"/sample%20job","lastStarted":"2026-01-02"}]}`)
		case "/proxy/artifactory/api/build/sample job/42":
			if r.URL.Query().Get("project") != "p" || r.URL.Query().Get("started") != "2026-01-01T12:00:00.000+0000" {
				t.Error("lost project or started parameter")
			}
			data, _ := os.ReadFile("../../testdata/build.json")
			w.Write(data)
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	client, err := newTestClient(settings(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	repos, err := client.ListRepositories(ctx, app.RepositoryFilter{Type: "local", PackageType: "generic", Project: "p"})
	if err != nil || len(repos.Repositories) != 1 {
		t.Fatalf("repository contract: %+v %v", repos, err)
	}
	results, err := client.Search(ctx, search.Filters{Limit: 1})
	if err != nil || results.Artifacts[0].Size != 9007199254740993 || len(results.Notices) != 1 {
		t.Fatalf("search contract: %+v %v", results, err)
	}
	in := app.ArtifactInput{Repository: "libs", Path: "目录/a space;tag.jar"}
	info, err := client.ArtifactInfo(ctx, in)
	if err != nil || info.Size != "9007199254740993" || info.Checksums.SHA256 != "abcdef" {
		t.Fatalf("storage contract: %+v %v", info, err)
	}
	folder, err := client.Folder(ctx, app.ArtifactInput{Repository: "libs"})
	if err != nil || len(folder.Children) != 2 {
		t.Fatalf("folder contract: %+v %v", folder, err)
	}
	props, err := client.Properties(ctx, app.PropertiesInput{Repository: in.Repository, Path: in.Path, Keys: []string{"wanted"}})
	if err != nil || len(props.Properties) != 1 {
		t.Fatalf("properties contract: %+v %v", props, err)
	}
	props, err = client.Properties(ctx, app.PropertiesInput{Repository: in.Repository, Path: in.Path})
	if err != nil || props.Properties["credential"][0] != "[REDACTED]" {
		t.Fatal("credential redaction failed", err)
	}
	stats, err := client.Stats(ctx, in)
	if err != nil || stats.DownloadCount != 9007199254740993 {
		t.Fatalf("stats contract: %+v %v", stats, err)
	}
	builds, err := client.ListBuilds(ctx, app.BuildsInput{Project: "p", Limit: 1, Offset: 1})
	if err != nil || builds.Builds[0].Name != "z-job" || *builds.Page.Total != 2 {
		t.Fatalf("build list contract: %+v %v", builds, err)
	}
	build, err := client.BuildInfo(ctx, app.BuildInput{Name: "sample job", Number: "42", Project: "p", Started: "2026-01-01T12:00:00.000+0000", DetailLimit: 1})
	if err != nil || len(build.Modules) != 1 || len(build.Modules[0].Artifacts) != 1 {
		t.Fatalf("build contract: %+v %v", build, err)
	}
	encoded, _ := json.Marshal(build)
	if strings.Contains(string(encoded), "must-be-excluded") {
		t.Fatal("build environment leaked")
	}
	before := requests.Load()
	_, err = client.ArtifactInfo(ctx, app.ArtifactInput{Repository: "other", Path: "a.jar"})
	if app.Classify(err).Category != "forbidden" || requests.Load() != before {
		t.Fatal("scope violation reached upstream")
	}
}
func TestFailuresAndBoundedReads(t *testing.T) {
	for code, category := range map[int]string{400: "invalid_input", 401: "unauthorized", 403: "forbidden", 404: "not_found", 429: "rate_limited", 500: "unavailable", 504: "timed_out"} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			s := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(code)
				io.WriteString(w, `{"errors":[{"message":"test-token secret upstream content"}]}`)
			}))
			defer s.Close()
			c, err := newTestClient(settings(s.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "a.jar"})
			if err == nil || app.Classify(err).Category != category || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("wrong failure: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("retries should remain disabled while transport controls are deferred")
			}
		})
	}
	for _, test := range []struct {
		name, body, category string
		limit                int64
	}{{"malformed", `{`, "unavailable", 100}, {"large", strings.Repeat("x", 1024), "response_too_large", 32}, {"null", `null`, "unavailable", 100}} {
		t.Run(test.name, func(t *testing.T) {
			s := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, test.body) }))
			defer s.Close()
			cfg := settings(s.URL)
			cfg.ResponseLimit = test.limit
			c, err := newTestClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "a.jar"})
			if err == nil || app.Classify(err).Category != test.category {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}
func TestCancellationAndRedirect(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	received := make(chan struct{})
	s := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(received)
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	defer s.Close()
	c, err := newTestClient(settings(s.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ArtifactInfo(ctx, app.ArtifactInput{Repository: "libs", Path: "a.jar"})
		done <- err
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("upstream was not called")
	}
	cancel()
	select {
	case err := <-done:
		if app.Classify(err).Category != "timed_out" {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("call did not cancel")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
	var leaked atomic.Bool
	target := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	other, err := newTestClient(settings(redirect.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "a.jar"})
	if err == nil || leaked.Load() {
		t.Fatal("SDK followed upstream redirect")
	}
}
func TestCustomCA(t *testing.T) {
	s := memoryTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"repo":"libs","path":"/a.jar","checksums":{}}`)
	}))
	defer s.Close()
	cfg := settings(s.URL)
	c, err := newTestClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "a.jar"})
	c.Close()
	if err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CAFile = ca
	c, err = newTestClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "a.jar"})
	if err != nil {
		t.Fatalf("custom CA was not trusted: %v", err)
	}
}

func TestClosedReadersAndDiscoveryPermissions(t *testing.T) {
	closed := make(chan struct{})
	s := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "api/repositories") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "large") {
			io.WriteString(w, strings.Repeat("x", 100))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(closed)
			return
		}
		io.WriteString(w, `{"repo":"libs","path":"/a.jar","checksums":{}}`)
	}))
	defer s.Close()
	cfg := settings(s.URL)
	cfg.ResponseLimit = 64
	c, err := newTestClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ListRepositories(context.Background(), app.RepositoryFilter{}); err == nil || app.Classify(err).Category != "forbidden" {
		t.Fatalf("discovery denial was hidden: %v", err)
	}
	if _, err := c.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "a.jar"}); err != nil {
		t.Fatalf("known repository should work without discovery: %v", err)
	}
	if _, err := c.ArtifactInfo(context.Background(), app.ArtifactInput{Repository: "libs", Path: "large"}); err == nil || app.Classify(err).Category != "response_too_large" {
		t.Fatalf("stream limit failed: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("oversized upstream response reader was not closed")
	}
}

func TestOperationAllowlist(t *testing.T) {
	for _, test := range [][2]string{{"delete", "api/storage/libs/a"}, {"artifact", "https://evil.test/api/storage/libs/a"}, {"artifact", "api/storage/libs/../a"}, {"artifact", "api/storage/libs/a?properties"}, {"folder", "api/storage/libs?list"}, {"build", "api/build/job/1?delete=true"}, {"aql", "api/search/aql?x=1"}} {
		if err := validateOperation(test[0], test[1]); err == nil {
			t.Fatalf("accepted unsupported operation %v", test)
		}
	}
}
