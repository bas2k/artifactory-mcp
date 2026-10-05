package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"artifactory-mcp/internal/jfrog"

	"github.com/jfrog/jfrog-client-go/utils/log"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("ARTIFACTORY_MCP_TEST_PROCESS") == "1" {
		os.Args = append([]string{os.Args[0]}, flag.Args()...)
		jfrog.ConfigureLogging()
		log.Output("smoke-secret-token SDK output")
		log.Error("smoke-secret-token SDK error")
		main()
		os.Exit(0)
	}
}
func TestStdioHasOnlyProtocolMessages(t *testing.T) {
	for _, test := range []struct {
		name string
		env  string
		args []string
		want int
	}{
		{name: "default", want: 8},
		{name: "environment", env: "list_builds,get_build_info", want: 6},
		{name: "flag overrides environment", env: "list_builds,get_build_info", args: []string{"--disable-tools=list_repositories"}, want: 7},
		{name: "empty flag clears environment", env: "list_builds,get_build_info", args: []string{"--disable-tools="}, want: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MCP_DISABLE_TOOLS", test.env)
			testStdioHasOnlyProtocolMessages(t, test.args, test.want)
		})
	}
}

func testStdioHasOnlyProtocolMessages(t *testing.T, args []string, wantTools int) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestProcessHelper$", "--"}, args...)...)
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("MCP_LOG_LEVEL", "debug")
	command.Env = append(os.Environ(), "ARTIFACTORY_MCP_TEST_PROCESS=1", "ARTIFACTORY_URL=https://example.test/proxy/artifactory", "ARTIFACTORY_ACCESS_TOKEN=smoke-secret-token", "ARTIFACTORY_REPOSITORIES=libs", "ARTIFACTORY_CA_FILE=", "ARTIFACTORY_ALLOW_HTTP=false", "ARTIFACTORY_REQUEST_TIMEOUT=30s", "ARTIFACTORY_RESPONSE_LIMIT=5242880")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(10*time.Second, func() { command.Process.Kill() })
	defer timer.Stop()
	scanner := bufio.NewScanner(output)
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"smoke\",\"version\":\"1\"}}}\n")
	if !scanner.Scan() || !json.Valid(scanner.Bytes()) {
		t.Fatal("stdout contained a log or initialization failed")
	}
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")
	if !scanner.Scan() {
		t.Fatal("tools/list returned no response")
	}
	var response struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.Result.Tools) != wantTools {
		t.Fatal("stdout was not a valid tools/list response", string(scanner.Bytes()))
	}
	input.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	if diagnostics.Len() == 0 || strings.Contains(diagnostics.String(), "smoke-secret-token") {
		t.Fatal("stderr missing logs or exposed token")
	}
}

func TestLogLevelControlsStartup(t *testing.T) {
	t.Setenv("MCP_DISABLE_TOOLS", "")
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessHelper$")
			command.Env = append(os.Environ(), "ARTIFACTORY_MCP_TEST_PROCESS=1", "MCP_TRANSPORT=stdio", "MCP_LOG_LEVEL="+level,
				"ARTIFACTORY_URL=https://example.test/artifactory", "ARTIFACTORY_ACCESS_TOKEN=smoke-secret-token",
				"ARTIFACTORY_REPOSITORIES=libs", "ARTIFACTORY_CA_FILE=", "ARTIFACTORY_ALLOW_HTTP=false",
				"ARTIFACTORY_REQUEST_TIMEOUT=30s", "ARTIFACTORY_RESPONSE_LIMIT=5242880")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatal(err, stderr.String())
			}
			wantStartup := level == "debug" || level == "info"
			if strings.Contains(stderr.String(), "starting Artifactory MCP server") != wantStartup {
				t.Fatalf("unexpected startup logging at %s: %s", level, stderr.String())
			}
			if stdout.Len() != 0 || strings.Contains(stderr.String(), "smoke-secret-token") {
				t.Fatal("logging polluted stdout or exposed credentials")
			}
		})
	}
}
