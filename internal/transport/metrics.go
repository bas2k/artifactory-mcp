package transport

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type metrics struct {
	registry     *prometheus.Registry
	requests     *prometheus.CounterVec
	requestTime  *prometheus.HistogramVec
	inFlight     prometheus.Gauge
	toolCalls    *prometheus.CounterVec
	toolCallTime *prometheus.HistogramVec
}

func newMetrics() *metrics {
	buckets := slices.Concat(prometheus.DefBuckets, []float64{30, 60, 120, 300, 600})
	m := &metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artifactory_mcp_http_requests_total",
			Help: "Completed requests to /mcp, including rejected requests.",
		}, []string{"method", "code"}),
		requestTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "artifactory_mcp_http_request_duration_seconds",
			Help: "Duration of requests to /mcp, including response streaming.", Buckets: buckets,
		}, []string{"method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "artifactory_mcp_http_requests_in_flight",
			Help: "Requests to /mcp currently being handled.",
		}),
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artifactory_mcp_tool_calls_total",
			Help: "Completed MCP tool calls by tool and outcome (success or error).",
		}, []string{"tool", "outcome"}),
		toolCallTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "artifactory_mcp_tool_call_duration_seconds",
			Help: "Duration of MCP tool calls by tool and outcome.", Buckets: buckets,
		}, []string{"tool", "outcome"}),
	}
	m.registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.requestTime, m.inFlight, m.toolCalls, m.toolCallTime)
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{MaxRequestsInFlight: 2, Timeout: 5 * time.Second})
}

func (m *metrics) instrumentHTTP(next http.Handler) http.Handler {
	next = promhttp.InstrumentHandlerCounter(m.requests, next)
	next = promhttp.InstrumentHandlerDuration(m.requestTime, next)
	return promhttp.InstrumentHandlerInFlight(m.inFlight, next)
}

func (m *metrics) instrumentTools(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		tool := "unknown"
		if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
			// Keep labels bounded, even when clients send arbitrary tool names.
			switch params.Name {
			case "get_server_info", "list_repositories", "search_artifacts", "search_artifacts_sorted", "get_artifact_info", "list_folder",
				"get_artifact_properties", "get_artifact_stats", "list_builds", "get_build_info", "list_build_runs", "search_packages", "list_package_versions":
				tool = params.Name
			}
		}
		start := time.Now()
		result, err := next(ctx, method, req)
		outcome := "success"
		if call, ok := result.(*mcp.CallToolResult); err != nil || !ok || call == nil || call.IsError {
			outcome = "error"
		}
		m.toolCalls.WithLabelValues(tool, outcome).Inc()
		m.toolCallTime.WithLabelValues(tool, outcome).Observe(time.Since(start).Seconds())
		return result, err
	}
}
