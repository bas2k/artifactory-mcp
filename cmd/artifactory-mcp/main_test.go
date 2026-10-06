package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"artifactory-mcp/internal/jfrog"

	"github.com/jfrog/jfrog-client-go/utils/log"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProcessHelper(_ *testing.T) {
	if os.Getenv("ARTIFACTORY_MCP_TEST_PROCESS") == "1" {
		os.Args = append([]string{os.Args[0]}, flag.Args()...)
		jfrog.ConfigureLogging()
		log.Output(os.Getenv("ARTIFACTORY_ACCESS_TOKEN"), "SDK output")
		log.Error(os.Getenv("ARTIFACTORY_ACCESS_TOKEN"), "SDK error")
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
		{name: "default", want: 13},
		{name: "environment", env: "list_builds,get_build_info", want: 11},
		{name: "flag overrides environment", env: "list_builds,get_build_info", args: []string{"--disable-tools=list_repositories"}, want: 12},
		{name: "empty flag clears environment", env: "list_builds,get_build_info", args: []string{"--disable-tools="}, want: 13},
		{name: "new tools disabled", env: "search_packages,list_package_versions,list_build_runs", want: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MCP_DISABLE_TOOLS", test.env)
			testStdioHasOnlyProtocolMessages(t, test.args, test.want)
		})
	}
}

func testStdioHasOnlyProtocolMessages(t *testing.T, args []string, wantTools int) {
	t.Helper()
	command := dotEnvProcess(t, t.TempDir(), args...)
	command.Env = append(command.Env, "MCP_TRANSPORT=stdio", "MCP_LOG_LEVEL=debug", "MCP_DISABLE_TOOLS="+os.Getenv("MCP_DISABLE_TOOLS"), "ARTIFACTORY_URL=https://example.test/proxy/artifactory", "ARTIFACTORY_ACCESS_TOKEN=smoke-secret-token", "ARTIFACTORY_REPOSITORIES=libs", "ARTIFACTORY_CA_FILE=", "ARTIFACTORY_ALLOW_HTTP=false", "ARTIFACTORY_REQUEST_TIMEOUT=30s", "ARTIFACTORY_RESPONSE_LIMIT=5242880")
	assertStdioProtocol(t, command, wantTools)
}

func assertStdioProtocol(t *testing.T, command *exec.Cmd, wantTools int) {
	t.Helper()
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
	scanner := bufio.NewScanner(output)
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"smoke\",\"version\":\"1\"}}}\n")
	if !scanner.Scan() || !json.Valid(scanner.Bytes()) {
		t.Fatal("stdout contained a log or initialization failed")
	}
	var initialized struct {
		Result mcp.InitializeResult `json:"result"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &initialized); err != nil || initialized.Result.Instructions == "" || initialized.Result.Capabilities.Resources == nil {
		t.Fatal("missing instructions or resources capability on stdio", string(scanner.Bytes()))
	}
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")
	if !scanner.Scan() {
		t.Fatal("tools/list returned no response")
	}
	var response struct {
		Result struct {
			Tools     []json.RawMessage       `json:"tools"`
			Resources []*mcp.Resource         `json:"resources"`
			Contents  []*mcp.ResourceContents `json:"contents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.Result.Tools) != wantTools {
		t.Fatal("stdout was not a valid tools/list response", string(scanner.Bytes()))
	}
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"resources/list\",\"params\":{}}\n")
	if !scanner.Scan() {
		t.Fatal("resources/list returned no response")
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.Result.Resources) != 1 || response.Result.Resources[0].URI != "artifactory://instructions" {
		t.Fatal("stdout was not a valid resources/list response", string(scanner.Bytes()))
	}
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"resources/read\",\"params\":{\"uri\":\"artifactory://instructions\"}}\n")
	if !scanner.Scan() {
		t.Fatal("resources/read returned no response")
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.Result.Contents) != 1 || response.Result.Contents[0].Text != initialized.Result.Instructions || response.Result.Contents[0].MIMEType != "text/markdown" {
		t.Fatal("stdout was not a valid resources/read response", string(scanner.Bytes()))
	}
	if strings.Contains(initialized.Result.Instructions, "smoke-secret-token") || strings.Contains(initialized.Result.Instructions, "dotenv-secret-token") {
		t.Fatal("instructions exposed credentials")
	}
	input.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	if !strings.Contains(diagnostics.String(), "SDK output") || !strings.Contains(diagnostics.String(), "SDK error") || strings.Contains(diagnostics.String(), "smoke-secret-token") || strings.Contains(diagnostics.String(), "dotenv-secret-token") {
		t.Fatal("stderr missing logs or exposed token")
	}
}

func TestLogLevelControlsStartup(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			command := dotEnvProcess(t, t.TempDir())
			command.Env = append(command.Env, "MCP_TRANSPORT=stdio", "MCP_LOG_LEVEL="+level,
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
