package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"artifactory-mcp/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func scrape(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/metrics", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("scrape failed: status=%d, content-type=%s", w.Code, w.Header().Get("Content-Type"))
	}
	return w.Body.String()
}

func TestMetricsEndpointGuards(t *testing.T) {
	for _, test := range []struct {
		name, method, token, origin string
		cfg                         config.HTTPConfig
		want                        int
	}{
		{name: "no auth configured", method: "GET", want: 200},
		{name: "metrics auth disabled with MCP auth", method: "GET", cfg: config.HTTPConfig{AuthToken: "inbound-secret"}, want: 200},
		{name: "missing token", method: "GET", cfg: config.HTTPConfig{MetricsAuthToken: "metrics-secret"}, want: 401},
		{name: "wrong token", method: "GET", token: "wrong", cfg: config.HTTPConfig{MetricsAuthToken: "metrics-secret"}, want: 401},
		{name: "authenticated", method: "GET", token: "metrics-secret", cfg: config.HTTPConfig{MetricsAuthToken: "metrics-secret"}, want: 200},
		{name: "MCP token rejected", method: "GET", token: "inbound-secret", cfg: config.HTTPConfig{AuthToken: "inbound-secret", MetricsAuthToken: "metrics-secret"}, want: 401},
		{name: "separate metrics token", method: "GET", token: "metrics-secret", cfg: config.HTTPConfig{AuthToken: "inbound-secret", MetricsAuthToken: "metrics-secret"}, want: 200},
		{name: "unlisted origin", method: "GET", origin: "https://evil.test", want: 403},
		{name: "allowed origin", method: "GET", origin: "https://app.test", cfg: config.HTTPConfig{AllowedOrigins: []string{"https://app.test"}}, want: 200},
		{name: "head", method: "HEAD", want: 200},
		{name: "post", method: "POST", want: 405},
		{name: "delete", method: "DELETE", want: 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &reader{}
			h := newHandler(t, r, test.cfg)
			req := httptest.NewRequest(test.method, "http://127.0.0.1:8080/metrics", nil)
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != test.want || r.calls.Load() != 0 {
				t.Fatalf("status=%d, reader calls=%d", w.Code, r.calls.Load())
			}
			if test.want == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
		})
	}
}

func TestMetricsTokenDoesNotAuthorizeMCP(t *testing.T) {
	for _, test := range []struct {
		name, token string
		cfg         config.HTTPConfig
		want        int
	}{
		{name: "MCP auth disabled", cfg: config.HTTPConfig{MetricsAuthToken: "metrics-secret"}, want: 200},
		{name: "missing MCP token", cfg: config.HTTPConfig{AuthToken: "inbound-secret", MetricsAuthToken: "metrics-secret"}, want: 401},
		{name: "metrics token rejected", token: "metrics-secret", cfg: config.HTTPConfig{AuthToken: "inbound-secret", MetricsAuthToken: "metrics-secret"}, want: 401},
		{name: "MCP token accepted", token: "inbound-secret", cfg: config.HTTPConfig{AuthToken: "inbound-secret", MetricsAuthToken: "metrics-secret"}, want: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &reader{}
			h := newHandler(t, r, test.cfg)
			req := request(context.Background(), "POST", callBody)
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != test.want {
				t.Fatalf("status=%d, want %d", w.Code, test.want)
			}
			if test.want == 401 && r.calls.Load() != 0 {
				t.Fatal("unauthorized request reached reader")
			}
		})
	}
}

func TestMetricsMalformedAuthorization(t *testing.T) {
	h := newHandler(t, &reader{}, config.HTTPConfig{MetricsAuthToken: "metrics-secret"})
	for _, values := range [][]string{{"Basic metrics-secret"}, {"Bearer"}, {"Bearer  metrics-secret"}, {"Bearer metrics-secret", "Bearer metrics-secret"}} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:8080/metrics", nil)
		for _, value := range values {
			req.Header.Add("Authorization", value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("malformed authorization accepted or missing challenge")
		}
		if strings.Contains(w.Body.String(), "metrics-secret") {
			t.Fatal("authentication error exposed metrics token")
		}
	}
}

func TestMetricsHTTPAndToolCalls(t *testing.T) {
	h := newHandler(t, &reader{}, config.HTTPConfig{AuthToken: "inbound-secret", MetricsAuthToken: "metrics-secret"})
	// An authentication rejection is an HTTP error, but never a tool call.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(context.Background(), "POST", callBody))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("missing authentication accepted")
	}
	httpClient, _, _ := startHTTP(t, h, time.Second)
	baseTransport := httpClient.Transport
	httpClient.Transport = bearerTransport{httpClient.Transport, "inbound-secret"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "metrics-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:8080/mcp", HTTPClient: httpClient, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, test := range []struct {
		name                     string
		arguments                map[string]any
		toolError, protocolError bool
	}{
		{name: "list_repositories", arguments: map[string]any{}},
		{name: "get_server_info", arguments: map[string]any{"unexpected": "private-value"}, toolError: true},
		{name: "search_artifacts_sorted", arguments: map[string]any{"limit": "invalid"}, toolError: true},
		{name: "get_artifact_info", arguments: map[string]any{"repository": "libs", "path": "private-artifact"}, toolError: true},
		{name: "list_repositories", arguments: map[string]any{"unexpected": "private-value"}, toolError: true},
		{name: "unknown-private-tool", arguments: map[string]any{}, protocolError: true},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.arguments})
		if (err != nil) != test.protocolError || (err == nil && (result == nil || result.IsError != test.toolError)) {
			t.Fatalf("call %s: result=%v, err=%v", test.name, result, err)
		}
	}
	// Scrape through the same listener that served MCP to check real HTTP framing.
	metricsClient := *httpClient
	metricsClient.Transport = bearerTransport{baseTransport, "metrics-secret"}
	resp, err := metricsClient.Get("http://127.0.0.1:8080/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status=%d, err=%v", resp.StatusCode, err)
	}
	for _, want := range []string{
		`artifactory_mcp_http_requests_total{code="401",method="post"} 1`,
		`artifactory_mcp_http_requests_total{code="200",method="post"}`,
		`artifactory_mcp_http_request_duration_seconds_count{method="post"}`,
		`artifactory_mcp_tool_calls_total{outcome="success",tool="list_repositories"} 1`,
		`artifactory_mcp_tool_calls_total{outcome="error",tool="list_repositories"} 1`,
		`artifactory_mcp_tool_calls_total{outcome="error",tool="get_artifact_info"} 1`,
		`artifactory_mcp_tool_calls_total{outcome="error",tool="get_server_info"} 1`,
		`artifactory_mcp_tool_calls_total{outcome="error",tool="search_artifacts_sorted"} 1`,
		`artifactory_mcp_tool_calls_total{outcome="error",tool="unknown"} 1`,
		`artifactory_mcp_tool_call_duration_seconds_count{outcome="success",tool="list_repositories"} 1`,
		`go_goroutines`,
		`process_cpu_seconds_total`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scrape missing %s", want)
		}
	}
	for _, sensitive := range []string{"inbound-secret", "metrics-secret", "private-artifact", "private-value", "unknown-private-tool"} {
		if strings.Contains(string(body), sensitive) {
			t.Errorf("metrics exposed %s", sensitive)
		}
	}
	// Registries belong to individual handlers and do not retain other servers' calls.
	fresh := scrape(t, newHandler(t, &reader{}, config.HTTPConfig{}), "")
	if strings.Contains(fresh, "artifactory_mcp_tool_calls_total{") || strings.Contains(fresh, "artifactory_mcp_http_requests_total{") {
		t.Fatal("metrics registry shared between handlers or scrapes counted as MCP traffic")
	}
}

func TestMetricsAvailableAtMCPConcurrencyLimit(t *testing.T) {
	r := &reader{started: make(chan struct{}, maxConcurrentRequests), release: make(chan struct{})}
	h := newHandler(t, r, config.HTTPConfig{})
	var wg sync.WaitGroup
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	defer func() { release(); wg.Wait() }()
	for range maxConcurrentRequests {
		wg.Go(func() { h.ServeHTTP(httptest.NewRecorder(), request(context.Background(), "POST", callBody)) })
	}
	for range maxConcurrentRequests {
		await(t, r.started)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(context.Background(), "POST", callBody))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("MCP concurrency limit not enforced")
	}
	body := scrape(t, h, "")
	for _, want := range []string{
		"artifactory_mcp_http_requests_in_flight 32",
		`artifactory_mcp_http_requests_total{code="503",method="post"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %s", want)
		}
	}
	release()
	wg.Wait()
	body = scrape(t, h, "")
	if !strings.Contains(body, "artifactory_mcp_http_requests_in_flight 0") ||
		!strings.Contains(body, `artifactory_mcp_tool_calls_total{outcome="success",tool="list_repositories"} 32`) {
		t.Fatal("in-flight gauge or concurrent tool counts incorrect")
	}
}
