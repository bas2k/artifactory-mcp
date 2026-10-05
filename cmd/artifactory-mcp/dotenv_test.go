package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDotEnv = `# Local configuration
export ARTIFACTORY_URL=https://example.test/artifactory
ARTIFACTORY_ACCESS_TOKEN='dotenv-secret-token'
MCP_TRANSPORT=stdio
MCP_LOG_LEVEL=error
MCP_DISABLE_TOOLS=list_builds,get_build_info
`

func TestDotEnvStartup(t *testing.T) {
	for _, test := range []struct {
		name string
		file string
		env  []string
		args []string
		want int
	}{
		{name: "file only", file: testDotEnv, want: 8},
		{name: "environment overrides file", file: testDotEnv + "ARTIFACTORY_URL=invalid\nARTIFACTORY_ACCESS_TOKEN='invalid token'\n", env: []string{"ARTIFACTORY_URL=https://example.test/artifactory", "ARTIFACTORY_ACCESS_TOKEN=smoke-secret-token", "MCP_DISABLE_TOOLS=list_repositories"}, want: 9},
		{name: "empty environment overrides file", file: testDotEnv, env: []string{"MCP_DISABLE_TOOLS="}, want: 10},
		{name: "flag overrides file", file: testDotEnv, args: []string{"--disable-tools=list_repositories"}, want: 9},
		{name: "empty flag overrides file", file: testDotEnv, args: []string{"--disable-tools="}, want: 10},
		{name: "missing file", env: []string{"ARTIFACTORY_URL=https://example.test/artifactory", "ARTIFACTORY_ACCESS_TOKEN=smoke-secret-token"}, want: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if test.file != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(test.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := dotEnvProcess(t, dir, test.args...)
			command.Env = append(command.Env, test.env...)
			assertStdioProtocol(t, command, test.want)
		})
	}
}

func TestDotEnvStartupErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		file      string
		directory bool
		env       []string
		want      string
	}{
		{name: "unterminated token", file: "ARTIFACTORY_ACCESS_TOKEN='dotenv-secret-token\n", want: "could not load .env"},
		{name: "invalid key", file: "BAD!KEY=dotenv-secret-token\n", want: "could not load .env"},
		{name: "unreadable file", directory: true, want: "could not load .env"},
		{name: "empty token overrides file", file: testDotEnv, env: []string{"ARTIFACTORY_ACCESS_TOKEN="}, want: "ARTIFACTORY_ACCESS_TOKEN must be a nonempty bearer token"},
		{name: "file values are validated", file: testDotEnv + "ARTIFACTORY_ALLOW_HTTP=invalid\n", want: "ARTIFACTORY_ALLOW_HTTP must be a boolean"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".env")
			if test.directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(test.file), 0600); err != nil {
				t.Fatal(err)
			}
			command := dotEnvProcess(t, dir)
			command.Env = append(command.Env, test.env...)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			var exitErr *exec.ExitError
			if err := command.Run(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("expected startup exit code 1, got %v", err)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("unexpected startup output: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), "dotenv-secret-token") || strings.Contains(stderr.String(), "smoke-secret-token") {
				t.Fatal("startup diagnostics exposed a token")
			}
		})
	}
}

func dotEnvProcess(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestProcessHelper$", "--"}, args...)...)
	command.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "ARTIFACTORY_") && !strings.HasPrefix(entry, "MCP_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "ARTIFACTORY_MCP_TEST_PROCESS=1")
	return command
}
