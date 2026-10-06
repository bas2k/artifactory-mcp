package jfrog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jfrog/jfrog-client-go/utils/log"
)

func TestSDKLoggingLevelsAndRedaction(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	ConfigureLogging("credential-secret", "other-secret")
	t.Cleanup(func() {
		diagnosticTokens.Delete("credential-secret")
		diagnosticTokens.Delete("other-secret")
	})
	for _, test := range []struct {
		level    slog.Level
		sdkLevel log.LevelType
		want     []string
	}{
		{slog.LevelDebug, log.DEBUG, []string{"DEBUG", "DEBUG", "INFO", "WARN", "ERROR", "ERROR"}},
		{slog.LevelInfo, log.INFO, []string{"INFO", "WARN", "ERROR", "ERROR"}},
		{slog.LevelWarn, log.WARN, []string{"WARN", "ERROR", "ERROR"}},
		{slog.LevelError, log.ERROR, []string{"ERROR", "ERROR"}},
	} {
		t.Run(test.level.String(), func(t *testing.T) {
			var output bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: test.level})))
			if got := log.GetLogger().GetLogLevel(); got != test.sdkLevel {
				t.Fatalf("SDK log level = %v, want %v", got, test.sdkLevel)
			}
			for _, emit := range []func(...interface{}){log.Verbose, log.Debug, log.Info, log.Warn, log.Error, log.Output} {
				emit("Sending HTTP GET request to:", "https://example.test/api/system/version", "credential-secret", "other-secret")
			}
			for _, sensitive := range []string{"credential-secret", "other-secret"} {
				if strings.Contains(output.String(), sensitive) {
					t.Fatal("SDK diagnostic exposed sensitive content")
				}
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != len(test.want) {
				t.Fatalf("got %d log entries, want %d: %s", len(lines), len(test.want), output.String())
			}
			for i, line := range lines {
				var entry struct{ Level, Msg string }
				if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Level != test.want[i] || entry.Msg != "Sending HTTP GET request to: https://example.test/api/system/version [REDACTED] [REDACTED]" {
					t.Fatalf("unexpected log entry: %s (%v)", line, err)
				}
			}
		})
	}
}
