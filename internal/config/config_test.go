package config

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestDisabledToolsConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		env     string
		args    []string
		want    []string
		wantErr bool
	}{
		{name: "default"},
		{name: "new tools", env: "get_server_info,search_artifacts_sorted", want: []string{"get_server_info", "search_artifacts_sorted"}},
		{name: "environment", env: " list_builds, get_build_info ", want: []string{"list_builds", "get_build_info"}},
		{name: "empty entries", env: " , list_builds,, ", want: []string{"list_builds"}},
		{name: "duplicates", env: "list_builds,list_builds", want: []string{"list_builds", "list_builds"}},
		{name: "flag", args: []string{"--disable-tools", "list_repositories,search_artifacts"}, want: []string{"list_repositories", "search_artifacts"}},
		{name: "flag overrides environment", env: "list_builds", args: []string{"--disable-tools=get_artifact_stats"}, want: []string{"get_artifact_stats"}},
		{name: "empty flag clears environment", env: "list_builds", args: []string{"--disable-tools="}},
		{name: "flag overrides invalid environment", env: "unknown", args: []string{"--disable-tools="}},
		{name: "unknown environment tool", env: "unknown", wantErr: true},
		{name: "unknown flag tool", args: []string{"--disable-tools=unknown"}, wantErr: true},
		{name: "case sensitive", env: "LIST_BUILDS", wantErr: true},
		{name: "missing flag value", args: []string{"--disable-tools"}, wantErr: true},
		{name: "unknown flag", args: []string{"--unknown"}, wantErr: true},
		{name: "positional argument", args: []string{"list_builds"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{
				"ARTIFACTORY_URL":          "https://example.test/artifactory",
				"ARTIFACTORY_ACCESS_TOKEN": "secret",
				"MCP_DISABLE_TOOLS":        test.env,
			}
			cfg, err := Load(func(k string) string { return env[k] }, test.args...)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, want error = %v", err, test.wantErr)
			}
			if err == nil && !slices.Equal(cfg.DisabledTools, test.want) {
				t.Fatalf("disabled tools = %v, want %v", cfg.DisabledTools, test.want)
			}
		})
	}
}

func TestLogLevelConfiguration(t *testing.T) {
	for _, test := range []struct {
		value string
		want  slog.Level
	}{{"", slog.LevelInfo}, {"debug", slog.LevelDebug}, {"info", slog.LevelInfo}, {"warn", slog.LevelWarn}, {"error", slog.LevelError}, {"DEBUG", slog.LevelDebug}} {
		t.Run(test.value, func(t *testing.T) {
			env := map[string]string{
				"ARTIFACTORY_URL":          "https://example.test/artifactory",
				"ARTIFACTORY_ACCESS_TOKEN": "secret",
				"MCP_LOG_LEVEL":            test.value,
			}
			cfg, err := Load(func(k string) string { return env[k] })
			if err != nil || cfg.LogLevel != test.want {
				t.Fatalf("level = %v, error = %v", cfg.LogLevel, err)
			}
		})
	}
	for _, value := range []string{"trace", "warning", "debug+1", "credential-secret"} {
		env := map[string]string{"MCP_LOG_LEVEL": value}
		_, err := Load(func(k string) string { return env[k] })
		if err == nil || strings.Contains(err.Error(), value) {
			t.Fatalf("invalid log level accepted or exposed: %v", err)
		}
	}
}

func TestLoad(t *testing.T) {
	env := map[string]string{"ARTIFACTORY_URL": "https://example.test/proxy/artifactory/", "ARTIFACTORY_ACCESS_TOKEN": "secret", "ARTIFACTORY_REPOSITORIES": "libs release,仓库"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://example.test/proxy/artifactory" || c.Timeout.Seconds() != 30 || c.ResponseLimit != 5<<20 || len(c.Repositories) != 2 {
		t.Fatalf("unexpected configuration: URL=%s", c.URL)
	}
	for key, value := range map[string]string{"ARTIFACTORY_URL": "https://user:secret@example.test/artifactory", "ARTIFACTORY_REQUEST_TIMEOUT": "0s", "ARTIFACTORY_RESPONSE_LIMIT": "-1", "ARTIFACTORY_REPOSITORIES": "good,../evil", "ARTIFACTORY_ACCESS_TOKEN": "secret\n"} {
		t.Run(key, func(t *testing.T) {
			copy := maps.Clone(env)
			copy[key] = value
			if _, err := Load(func(k string) string { return copy[k] }); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
func TestURLValidation(t *testing.T) {
	for _, value := range []string{"http://example.test/artifactory", "https://example.test/artifactory?x=1", "https://example.test/artifactory#", "https://example.test/a/../artifactory", "https://example.test/a/%2e%2e/artifactory", "https:///artifactory"} {
		env := map[string]string{
			"ARTIFACTORY_URL":          value,
			"ARTIFACTORY_ACCESS_TOKEN": "token",
		}
		_, err := Load(func(k string) string { return env[k] })
		if err == nil {
			t.Errorf("accepted invalid URL %q", value)
		}
	}
	env := map[string]string{
		"ARTIFACTORY_URL":          "http://localhost:8081/proxy/artifactory",
		"ARTIFACTORY_ACCESS_TOKEN": "token",
		"ARTIFACTORY_ALLOW_HTTP":   "true",
	}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil || !c.AllowHTTP {
		t.Fatal("explicit development HTTP rejected")
	}
}
func TestPaths(t *testing.T) {
	for _, path := range []string{"a/../b", "/a", "a//b", "a/", "https://evil.test/a", "a/%2fescape", "a\\b", "a\x00b"} {
		if ValidatePath(path, false) == nil {
			t.Errorf("accepted %q", path)
		}
	}
	for _, path := range []string{"folder/file with spaces.jar", "目录/文件.jar", "a;b/name+tag.jar"} {
		if err := ValidatePath(path, false); err != nil {
			t.Errorf("rejected legitimate name %q", path)
		}
	}
}
