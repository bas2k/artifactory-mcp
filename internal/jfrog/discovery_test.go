package jfrog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	app "artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/search"
)

func TestPackageAndBuildRunHTTPContracts(t *testing.T) {
	var requests atomic.Int32
	server := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authenticated SDK request")
		}
		switch r.URL.Path {
		case "/proxy/artifactory/api/search/aql":
			body, _ := io.ReadAll(r.Body)
			_, output, ok := strings.Cut(string(body), ").include(")
			if !ok || strings.Contains(output, "@") || !strings.Contains(output, `"property.*"`) {
				// Model the OSS rejection of filtered property output.
				http.Error(w, "Filtering properties result is not supported by AQL in the open source version", http.StatusBadRequest)
				return
			}
			if r.Method != "POST" || r.Header.Get("Content-Type") != "text/plain" || r.URL.RawQuery != "" || !strings.Contains(string(body), `"repo":"libs"`) || strings.Contains(string(body), ".limit(") || strings.Contains(string(body), ".transitive(") {
				t.Errorf("invalid package search: %s %s %s", r.Method, r.URL, body)
			}
			io.WriteString(w, `{"results":[{"repo":"libs","path":".","name":"pkg.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.version","value":"1.0"},{"key":"unrelated","value":"test-token"}]}],"notices":["test-token"]}`)
		case "/proxy/artifactory/api/build/sample job":
			if r.Method != "GET" || r.URL.Query().Get("project") != "p" || len(r.URL.Query()) != 1 || r.URL.EscapedPath() != "/proxy/artifactory/api/build/sample%20job" {
				t.Errorf("invalid build runs request: %s %s", r.Method, r.URL)
			}
			io.WriteString(w, `{"buildsNumbers":[{"uri":"/test-token","started":"2026-01-01T12:00:00.000+0000"},{"uri":"/42","started":"2026-01-02T12:00:00Z"}]}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := newTestClient(t, settings(server.URL))
	packages, err := c.SearchPackages(context.Background(), search.PackageFilters{PackageType: "npm", Limit: 1})
	if err != nil || len(packages.Packages) != 1 || len(packages.Notices) != 1 || packages.Notices[0] != "[REDACTED]" {
		t.Fatalf("package contract: %+v %v", packages, err)
	}
	versions, err := c.ListPackageVersions(context.Background(), app.PackageVersionsInput{PackageType: "npm", Name: "widget"})
	if err != nil || len(versions.Versions) != 1 {
		t.Fatalf("versions contract: %+v %v", versions, err)
	}
	runs, err := c.ListBuildRuns(context.Background(), app.BuildRunsInput{Name: "sample job", Project: "p", Limit: 1, Offset: 1})
	if err != nil || len(runs.Runs) != 1 || runs.Runs[0].Number != "[REDACTED]" || *runs.Page.Total != 2 {
		t.Fatalf("runs contract: %+v %v", runs, err)
	}
	encoded, _ := json.Marshal([]any{packages, versions, runs})
	if strings.Contains(string(encoded), "test-token") || strings.Contains(string(encoded), "unrelated") {
		t.Fatal("credential or unrelated metadata exposed")
	}
	before := requests.Load()
	_, err = c.ListPackageVersions(context.Background(), app.PackageVersionsInput{PackageType: "npm", Name: "widget", Repositories: []string{"other"}})
	if err == nil || app.Classify(err).Category != "forbidden" || requests.Load() != before {
		t.Fatal("package scope violation reached upstream")
	}
}

func TestDiscoveryResponseBounds(t *testing.T) {
	for _, mode := range []string{"search_packages", "list_package_versions", "list_build_runs"} {
		t.Run(mode, func(t *testing.T) {
			server := memoryServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 1024)) }))
			defer server.Close()
			cfg := settings(server.URL)
			cfg.ResponseLimit = 32
			c := newTestClient(t, cfg)
			var err error
			switch mode {
			case "search_packages":
				_, err = c.SearchPackages(context.Background(), search.PackageFilters{PackageType: "npm"})
			case "list_package_versions":
				_, err = c.ListPackageVersions(context.Background(), app.PackageVersionsInput{PackageType: "npm", Name: "widget"})
			case "list_build_runs":
				_, err = c.ListBuildRuns(context.Background(), app.BuildRunsInput{Name: "job"})
			}
			if err == nil || app.Classify(err).Category != "response_too_large" {
				t.Fatalf("response limit bypassed: %v", err)
			}
		})
	}
}

func TestBuildRunsOperationAllowlist(t *testing.T) {
	if err := validateOperation("build_runs", "api/build/sample%20job?project=p"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"api/build", "api/build/job/42", "api/build/../job", "api/build/job?started=x", "api/build/job?delete=true", "api/build/job?project=p&project=q", "https://evil.test/api/build/job", "api/build/%2e%2e", "api/build/job%2F42"} {
		if err := validateOperation("build_runs", path); err == nil {
			t.Fatalf("accepted build runs endpoint: %s", path)
		}
	}
}
