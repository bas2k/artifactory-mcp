package transport

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reader struct {
	artifactory.Reader
	calls    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (r *reader) ListRepositories(ctx context.Context, _ artifactory.RepositoryFilter) (artifactory.Repositories, error) {
	r.calls.Add(1)
	if r.started != nil {
		r.started <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			if r.canceled != nil {
				r.canceled <- struct{}{}
			}
			return artifactory.Repositories{}, ctx.Err()
		}
	}
	return artifactory.Repositories{Repositories: []artifactory.Repository{{Key: "libs", Type: "LOCAL", PackageType: "Generic"}}}, nil
}

func (*reader) ArtifactInfo(context.Context, artifactory.ArtifactInput) (artifactory.ArtifactInfo, error) {
	return artifactory.ArtifactInfo{}, artifactory.NewError("forbidden", "token lacks permission")
}

func newHandler(t *testing.T, r *reader, cfg config.HTTPConfig) http.Handler {
	t.Helper()
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8080"
	}
	h, err := NewHTTPHandler(mcpserver.New(r, "test"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Use real HTTP framing over net.Pipe to avoid requiring socket listen rights.
type pipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (*pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080} }

func startHTTP(t *testing.T, handler http.Handler, grace time.Duration) (*http.Client, context.CancelFunc, <-chan error) {
	t.Helper()
	l := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- serveHTTP(ctx, l, handler, grace); close(finished) }()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case l.connections <- server:
			return client, nil
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, ctx.Err()
		case <-l.done:
			client.Close()
			server.Close()
			return nil, net.ErrClosed
		}
	}}
	t.Cleanup(func() {
		cancel()
		tr.CloseIdleConnections()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("HTTP runner did not stop")
		}
	})
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}, cancel, finished
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func TestHTTPProtocol(t *testing.T) {
	for _, token := range []string{"", "inbound-secret"} {
		t.Run("token="+token, func(t *testing.T) {
			r := &reader{}
			h := newHandler(t, r, config.HTTPConfig{AuthToken: token})
			httpClient, _, _ := startHTTP(t, h, time.Second)
			if token != "" {
				httpClient.Transport = bearerTransport{httpClient.Transport, token}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:8080/mcp", HTTPClient: httpClient, MaxRetries: -1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) != 10 {
				t.Fatalf("got %d tools", len(tools.Tools))
			}
			for _, tool := range tools.Tools {
				if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
					t.Fatalf("invalid tool metadata: %s", tool.Name)
				}
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
			if err != nil || result == nil || result.IsError {
				t.Fatalf("call: %v, %+v", err, result)
			}
			if result.StructuredContent == nil || len(result.Content) != 1 {
				t.Fatal("missing structured output")
			}
			var out artifactory.Repositories
			if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &out); err != nil || len(out.Repositories) != 1 || out.Repositories[0].Key != "libs" {
				t.Fatal("incorrect result", err)
			}
			for _, test := range []struct {
				name     string
				args     map[string]any
				category string
			}{
				{"list_repositories", map[string]any{"type": 42}, "invalid_input"},
				{"get_artifact_info", map[string]any{"repository": "libs", "path": "a.jar"}, "forbidden"},
			} {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
				if err != nil || result == nil || !result.IsError {
					t.Fatalf("expected tool error: %v", err)
				}
				var out artifactory.Error
				if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &out); err != nil || out.Category != test.category {
					t.Fatal("incorrect error", err)
				}
			}
			if r.calls.Load() != 1 {
				t.Fatal("invalid request reached reader")
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					client := mcp.NewClient(&mcp.Implementation{Name: "concurrent", Version: "1"}, nil)
					s, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:8080/mcp", HTTPClient: httpClient, MaxRetries: -1}, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer s.Close()
					result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
					if err != nil || result == nil || result.IsError {
						t.Error("concurrent call failed", err)
					}
				})
			}
			wg.Wait()
			if r.calls.Load() != 9 {
				t.Fatal("concurrent calls missing")
			}
		})
	}
}

const callBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_repositories","arguments":{}}}`

func request(ctx context.Context, method, body string) *http.Request {
	r, _ := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:8080/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	return r
}

func TestHTTPGuards(t *testing.T) {
	for _, test := range []struct {
		name, method, body, auth string
		origins                  []string
		cfg                      config.HTTPConfig
		want                     int
	}{
		{name: "optional auth", method: "POST", body: callBody, want: 200},
		{name: "missing auth", method: "POST", cfg: config.HTTPConfig{AuthToken: "inbound-secret"}, want: 401},
		{name: "bad auth", method: "POST", auth: "Bearer wrong", cfg: config.HTTPConfig{AuthToken: "inbound-secret"}, want: 401},
		{name: "valid auth", method: "POST", body: callBody, auth: "bearer inbound-secret", cfg: config.HTTPConfig{AuthToken: "inbound-secret"}, want: 200},
		{name: "GET authenticated", method: "GET", cfg: config.HTTPConfig{AuthToken: "inbound-secret"}, want: 401},
		{name: "DELETE authenticated", method: "DELETE", cfg: config.HTTPConfig{AuthToken: "inbound-secret"}, want: 401},
		{name: "default origin deny", method: "POST", origins: []string{"https://app.test"}, want: 403},
		{name: "empty origin deny", method: "POST", origins: []string{""}, want: 403},
		{name: "null origin deny", method: "POST", origins: []string{"null"}, want: 403},
		{name: "allowed origin", method: "POST", body: callBody, origins: []string{"https://app.test"}, cfg: config.HTTPConfig{AllowedOrigins: []string{"https://app.test"}}, want: 200},
		{name: "origin exact match", method: "POST", origins: []string{"https://app.test.evil"}, cfg: config.HTTPConfig{AllowedOrigins: []string{"https://app.test"}}, want: 403},
		{name: "duplicate origin", method: "POST", origins: []string{"https://app.test", "https://app.test"}, cfg: config.HTTPConfig{AllowedOrigins: []string{"https://app.test"}}, want: 403},
		{name: "GET origin deny", method: "GET", origins: []string{"https://evil.test"}, want: 403},
		{name: "DELETE origin deny", method: "DELETE", origins: []string{"https://evil.test"}, want: 403},
		{name: "stateless GET", method: "GET", want: 405},
		{name: "unknown method", method: "PUT", want: 405},
		{name: "malformed JSON", method: "POST", body: "{", want: 400},
		{name: "initialized notification", method: "POST", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`, want: 202},
		{name: "oversized body", method: "POST", body: strings.Repeat("x", maxRequestBytes+1), want: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &reader{}
			h := newHandler(t, r, test.cfg)
			req := request(context.Background(), test.method, test.body)
			if test.auth != "" {
				req.Header.Set("Authorization", test.auth)
			}
			if test.origins != nil {
				req.Header["Origin"] = test.origins
			}
			// Exercise body limiting without relying on Content-Length.
			req.ContentLength = -1
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != test.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if test.want == 200 && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatal("missing SSE response")
			}
			if test.want == 401 && w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing authentication challenge")
			}
			if strings.Contains(w.Body.String(), "inbound-secret") {
				t.Fatal("credential exposed")
			}
			if test.want != 200 && r.calls.Load() != 0 {
				t.Fatal("rejected request reached reader")
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"unsupported protocol", func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "invalid") }, 400},
		{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"missing Accept", func(r *http.Request) { r.Header.Del("Accept") }, 400},
		{"duplicate auth", func(r *http.Request) { r.Header.Add("Authorization", "Bearer inbound-secret") }, 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &reader{}
			h := newHandler(t, r, config.HTTPConfig{AuthToken: "inbound-secret"})
			req := request(context.Background(), "POST", callBody)
			req.Header.Set("Authorization", "Bearer inbound-secret")
			test.mutate(req)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != test.want || r.calls.Load() != 0 {
				t.Fatalf("status %d, calls %d", w.Code, r.calls.Load())
			}
			if strings.Contains(w.Body.String(), "inbound-secret") {
				t.Fatal("credential exposed")
			}
		})
	}
	h := newHandler(t, &reader{}, config.HTTPConfig{})
	for _, path := range []string{"/", "/mcp/", "/other"} {
		w := httptest.NewRecorder()
		r := request(context.Background(), "POST", callBody)
		r.URL.Path = path
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("path %s: status %d", path, w.Code)
		}
	}
	local := request(context.Background(), "POST", callBody)
	local.Host = "evil.test"
	local = local.WithContext(context.WithValue(local.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, local)
	if w.Code != 403 {
		t.Fatal("SDK localhost protection disabled")
	}
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for request")
	}
}

func TestHTTPCancellationAndShutdown(t *testing.T) {
	for _, mode := range []string{"disconnect", "graceful shutdown", "forced shutdown"} {
		t.Run(mode, func(t *testing.T) {
			r := &reader{started: make(chan struct{}, 1), release: make(chan struct{}), canceled: make(chan struct{}, 1)}
			h := newHandler(t, r, config.HTTPConfig{})
			grace := time.Second
			if mode == "forced shutdown" {
				grace = 20 * time.Millisecond
			}
			client, stopServer, finished := startHTTP(t, h, grace)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response := make(chan error, 1)
			go func() {
				resp, err := client.Do(request(ctx, "POST", callBody))
				if err == nil {
					_, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				response <- err
			}()
			await(t, r.started)
			switch mode {
			case "disconnect":
				cancel()
				await(t, r.canceled)
			case "graceful shutdown":
				stopServer()
				select {
				case <-finished:
					t.Fatal("shutdown did not drain active request")
				default:
				}
				close(r.release)
			case "forced shutdown":
				stopServer()
				await(t, r.canceled)
			}
			select {
			case err := <-response:
				if mode == "graceful shutdown" && err != nil {
					t.Fatal("drained request failed", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("request did not finish")
			}
			if mode != "disconnect" {
				select {
				case err := <-finished:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("shutdown did not finish")
				}
			}
		})
	}
}

func TestHTTPConcurrencyLimit(t *testing.T) {
	r := &reader{started: make(chan struct{}, maxConcurrentRequests), release: make(chan struct{})}
	h := newHandler(t, r, config.HTTPConfig{})
	var wg sync.WaitGroup
	defer func() { close(r.release); wg.Wait() }()
	for range maxConcurrentRequests {
		wg.Go(func() { h.ServeHTTP(httptest.NewRecorder(), request(context.Background(), "POST", callBody)) })
	}
	for range maxConcurrentRequests {
		await(t, r.started)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(context.Background(), "POST", callBody))
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal("concurrency limit not enforced")
	}
	if r.calls.Load() != maxConcurrentRequests {
		t.Fatal("over-limit request reached reader")
	}
}
