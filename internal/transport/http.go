package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"artifactory-mcp/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxRequestBytes       = 1 << 20
	maxConcurrentRequests = 32
	shutdownTimeout       = 10 * time.Second
)

type requestContextKey struct{}

// NewHTTPHandler serves stateless MCP and Prometheus endpoints. Construct it once per server.
func NewHTTPHandler(server *mcp.Server, cfg config.HTTPConfig) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	metrics := newMetrics()
	server.AddReceivingMiddleware(metrics.instrumentTools)
	metricsHandler := metrics.handler()
	// The SDK detaches RPC contexts from HTTP cancellation. Bridge the
	// originating request's cancellation into each RPC, including tool calls.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if incoming, ok := ctx.Value(requestContextKey{}).(context.Context); ok {
				ctx, cancel := context.WithCancel(ctx)
				stop := context.AfterFunc(incoming, cancel)
				defer stop()
				defer cancel()
				if incoming.Err() != nil {
					cancel()
				}
				return next(ctx, method, req)
			}
			return next(ctx, method, req)
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	origins := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		origins[origin] = true
	}
	expectedToken := sha256.Sum256([]byte(cfg.AuthToken))
	expectedMetricsToken := sha256.Sum256([]byte(cfg.MetricsAuthToken))
	slots := make(chan struct{}, maxConcurrentRequests)
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" && r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if values, present := r.Header["Origin"]; present && (len(values) != 1 || !origins[values[0]]) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		authRequired, expected := cfg.AuthToken != "", expectedToken
		if r.URL.Path == "/metrics" {
			authRequired, expected = cfg.MetricsAuthToken != "", expectedMetricsToken
		}
		if authRequired {
			values := r.Header.Values("Authorization")
			parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
			if len(values) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				unauthorized(w)
				return
			}
			provided := sha256.Sum256([]byte(parts[1]))
			if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
				unauthorized(w)
				return
			}
		}
		if r.URL.Path == "/metrics" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			metricsHandler.ServeHTTP(w, r)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many concurrent requests", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodPost {
			// Read here so oversized chunked bodies also get 413. The SDK
			// otherwise converts body read errors to 400.
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "cannot read request body", http.StatusBadRequest)
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, r.Context()))
		handler.ServeHTTP(w, r)
	})
	instrumented := metrics.instrumentHTTP(guarded)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			instrumented.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	}), nil
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="artifactory-mcp"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func RunHTTP(ctx context.Context, server *mcp.Server, cfg config.HTTPConfig) error {
	handler, err := NewHTTPHandler(server, cfg)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen for MCP HTTP: %w", err)
	}
	slog.Info("serving MCP HTTP", "address", listener.Addr().String(), "path", "/mcp", "metrics_path", "/metrics", "authentication", cfg.AuthToken != "", "metrics_authentication", cfg.MetricsAuthToken != "")
	return serveHTTP(ctx, listener, handler, shutdownTimeout)
}

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler, grace time.Duration) error {
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	var mu sync.Mutex
	var active sync.WaitGroup
	stopped := false
	httpServer := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return requests },
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if stopped {
				mu.Unlock()
				http.Error(w, "server shutting down", http.StatusServiceUnavailable)
				return
			}
			active.Add(1)
			mu.Unlock()
			defer active.Done()
			handler.ServeHTTP(w, r)
		}),
	}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-done:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), grace)
		err := httpServer.Shutdown(shutdown)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			serveErr = err
		}
	}
	mu.Lock()
	stopped = true
	mu.Unlock()
	cancelRequests()
	httpServer.Close()
	active.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}
