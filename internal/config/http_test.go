package config

import (
	"strings"
	"testing"
)

func TestTransportConfiguration(t *testing.T) {
	base := map[string]string{"ARTIFACTORY_URL": "https://example.test/artifactory", "ARTIFACTORY_ACCESS_TOKEN": "upstream-secret"}
	for _, test := range []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{name: "default stdio"},
		{name: "HTTP defaults", env: map[string]string{"MCP_TRANSPORT": "streamable-http"}},
		{name: "metrics authentication", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_METRICS_AUTH_TOKEN": "metrics-secret"}},
		{name: "unauthenticated public address", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_HTTP_ADDR": "0.0.0.0:8080"}},
		{name: "IPv6", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_HTTP_ADDR": "[::1]:8080"}},
		{name: "origins", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_HTTP_ALLOWED_ORIGINS": "https://app.test, http://localhost:3000", "MCP_HTTP_AUTH_TOKEN": "inbound-secret"}},
		{name: "ignored stdio HTTP settings", env: map[string]string{"MCP_HTTP_ADDR": "invalid", "MCP_HTTP_AUTH_TOKEN": "bad\n", "MCP_METRICS_AUTH_TOKEN": "bad\n", "MCP_HTTP_ALLOWED_ORIGINS": "*"}},
		{name: "unknown transport", env: map[string]string{"MCP_TRANSPORT": "sse"}, wantErr: true},
		{name: "bad address", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_HTTP_ADDR": "localhost"}, wantErr: true},
		{name: "bad port", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_HTTP_ADDR": "localhost:65536"}, wantErr: true},
		{name: "bad token", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_HTTP_AUTH_TOKEN": "inbound-secret\n"}, wantErr: true},
		{name: "bad metrics token", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_METRICS_AUTH_TOKEN": "metrics-secret\n"}, wantErr: true},
		{name: "metrics token with spaces", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_METRICS_AUTH_TOKEN": "metrics secret"}, wantErr: true},
		{name: "metrics token with non ASCII", env: map[string]string{"MCP_TRANSPORT": "streamable-http", "MCP_METRICS_AUTH_TOKEN": "metrics-secreté"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Load(func(k string) string {
				if value, ok := test.env[k]; ok {
					return value
				}
				return base[k]
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("Load error = %v", err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("validation exposed a credential")
				}
				return
			}
			if cfg.Transport == "streamable-http" && cfg.HTTP.Addr == "" {
				t.Fatal("missing default listener")
			}
			if test.name == "default stdio" && cfg.Transport != "stdio" {
				t.Fatal("default transport changed")
			}
			if test.name == "HTTP defaults" && (cfg.HTTP.Addr != "127.0.0.1:8080" || cfg.HTTP.AuthToken != "" || cfg.HTTP.MetricsAuthToken != "") {
				t.Fatal("incorrect HTTP defaults")
			}
			if test.name == "metrics authentication" && (cfg.HTTP.MetricsAuthToken != "metrics-secret" || cfg.HTTP.AuthToken != "") {
				t.Fatal("metrics authentication not loaded independently")
			}
			if test.name == "origins" && (len(cfg.HTTP.AllowedOrigins) != 2 || cfg.HTTP.AllowedOrigins[1] != "http://localhost:3000") {
				t.Fatal("origins not parsed")
			}
		})
	}
}

func TestHTTPOriginValidation(t *testing.T) {
	for _, origin := range []string{"", "*", "https://*.test", "null", "ftp://app.test", "https://app.test/", "https://user:secret@app.test", "https://app.test?", "https://app.test#", "https://app.test/path", "https://app.test,", "https://app.test:", "https://app.test:99999"} {
		t.Run(origin, func(t *testing.T) {
			if err := (HTTPConfig{Addr: "127.0.0.1:8080", AllowedOrigins: []string{origin}}).Validate(); err == nil {
				t.Fatal("invalid origin accepted")
			}
		})
	}
}
