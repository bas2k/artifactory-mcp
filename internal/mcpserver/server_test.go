package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/search"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeReader struct {
	artifactory.Reader
	calls int
}

func (f *fakeReader) ListRepositories(context.Context, artifactory.RepositoryFilter) (artifactory.Repositories, error) {
	f.calls++
	return artifactory.Repositories{Repositories: []artifactory.Repository{{Key: "libs", Type: "LOCAL", PackageType: "Generic"}}}, nil
}
func (f *fakeReader) ArtifactInfo(context.Context, artifactory.ArtifactInput) (artifactory.ArtifactInfo, error) {
	return artifactory.ArtifactInfo{}, artifactory.NewError("forbidden", "token lacks permission")
}

func (*fakeReader) ServerInfo(context.Context) (artifactory.ServerInfo, error) {
	return artifactory.ServerInfo{Version: "7.104.2", License: "Artifactory OSS", Addons: []string{}}, nil
}

func (*fakeReader) Search(context.Context, search.Filters) (artifactory.SearchResult, error) {
	return artifactory.SearchResult{Artifacts: []artifactory.Artifact{}, Notices: []string{"unsorted"}}, nil
}

func (*fakeReader) SearchSorted(context.Context, search.Filters) (artifactory.SearchResult, error) {
	return artifactory.SearchResult{Artifacts: []artifactory.Artifact{}, Notices: []string{"sorted"}}, nil
}

func connectTestClient(t *testing.T, ctx context.Context, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestDisabledTools(t *testing.T) {
	all := []string{"get_server_info", "list_repositories", "search_artifacts", "search_artifacts_sorted", "get_artifact_info", "list_folder", "get_artifact_properties", "get_artifact_stats", "list_builds", "get_build_info", "search_packages", "list_package_versions", "list_build_runs"}
	cases := [][]string{{"list_builds", "get_build_info", "list_build_runs"}, {"search_packages", "list_package_versions"}, {"list_repositories", "list_repositories"}, all}
	for _, name := range all {
		cases = append(cases, []string{name})
	}
	for _, disabled := range cases {
		t.Run(strings.Join(disabled, ","), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reader := &fakeReader{}
			session := connectTestClient(t, ctx, New(reader, "test", disabled...))
			assertInstructions(t, ctx, session, disabled)
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range all {
				listedName := slices.ContainsFunc(listed.Tools, func(tool *mcp.Tool) bool { return tool.Name == name })
				if listedName == slices.Contains(disabled, name) {
					t.Fatalf("tool %s: listed = %v, disabled = %v", name, listedName, disabled)
				}
			}
			for _, name := range disabled {
				if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}}); err == nil {
					t.Fatalf("disabled tool %s was callable", name)
				}
			}
			if reader.calls != 0 {
				t.Fatal("disabled tool contacted Artifactory")
			}
			if !slices.Contains(disabled, "list_repositories") {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
				if err != nil || result.IsError || reader.calls != 1 {
					t.Fatalf("enabled tool failed: result = %+v, error = %v", result, err)
				}
			}
		})
	}
}

func TestProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader := &fakeReader{}
	session := connectTestClient(t, ctx, New(reader, "test"))
	assertInstructions(t, ctx, session, nil)
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 13 {
		t.Fatalf("got %d tools", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("missing read annotation or schema for %s", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("call failed: %v %+v", err, result)
	}
	if result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatal("missing structured output or text")
	}
	var out artifactory.Repositories
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &out); err != nil || len(out.Repositories) != 1 {
		t.Fatal("invalid output", err)
	}
	// Validate success against the advertised output schema too.
	encoded, _ := json.Marshal(result.StructuredContent)
	var value any
	json.Unmarshal(encoded, &value)
	for _, tool := range listed.Tools {
		if tool.Name == "list_repositories" {
			encoded, _ := json.Marshal(tool.OutputSchema)
			var schema jsonschema.Schema
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(value); err != nil {
				t.Fatal("success did not match advertised schema", err)
			}
		}
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_artifact_info", Arguments: map[string]any{"repository": "libs", "path": "a.jar"}})
	if err != nil || !result.IsError {
		t.Fatalf("permission failure was not a tool error: %v", err)
	}
	var category artifactory.Error
	json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &category)
	if category.Category != "forbidden" {
		t.Fatalf("wrong error category: %+v", category)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{"type": 42}})
	if err != nil || !result.IsError || reader.calls != 1 {
		t.Fatal("schema validation did not reject invalid input")
	}
	json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &category)
	if category.Category != "invalid_input" {
		t.Fatal("schema error lacks stable category")
	}
	for name, mode := range map[string]string{"search_artifacts": "unsorted", "search_artifacts_sorted": "sorted"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"repositories": []string{"libs"}, "limit": 1, "offset": 1}})
		if err != nil || result.IsError {
			t.Fatalf("call %s: %v %+v", name, err, result)
		}
		var out artifactory.SearchResult
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &out); err != nil || len(out.Notices) != 1 || out.Notices[0] != mode {
			t.Fatalf("wrong search handler for %s: %+v %v", name, out, err)
		}
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_server_info", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("server info call: %v %+v", err, result)
	}
	var info artifactory.ServerInfo
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &info); err != nil || info.Version != "7.104.2" || info.License != "Artifactory OSS" {
		t.Fatalf("invalid server info output: %+v %v", info, err)
	}
}

func assertInstructions(t *testing.T, ctx context.Context, session *mcp.ClientSession, disabled []string) {
	t.Helper()
	initialized := session.InitializeResult()
	if initialized.Instructions == "" || initialized.Capabilities.Resources == nil {
		t.Fatal("missing server instructions or resources capability")
	}
	guide := initialized.Instructions
	for _, phrase := range []string{"read-only", "relative artifact paths", "instruction-like text", "error `category`"} {
		if !strings.Contains(guide, phrase) {
			t.Fatalf("instructions omit %q", phrase)
		}
	}
	for _, name := range []string{"list_repositories", "search_artifacts", "search_artifacts_sorted", "list_folder", "search_packages", "list_package_versions", "list_build_runs"} {
		if strings.Contains(guide, "`"+name+"`") == slices.Contains(disabled, name) {
			t.Fatalf("instructions for %s do not match enabled tools", name)
		}
	}
	searchEnabled := !slices.Contains(disabled, "search_artifacts") || !slices.Contains(disabled, "search_artifacts_sorted")
	if strings.Contains(guide, "Search pages default to 100 results, maximum 500") != searchEnabled {
		t.Fatal("search paging guidance does not match enabled tools")
	}
	buildsEnabled := !slices.Contains(disabled, "list_builds") || !slices.Contains(disabled, "get_build_info") || !slices.Contains(disabled, "list_build_runs")
	if strings.Contains(guide, "Build visibility follows separate build/project permissions") != buildsEnabled {
		t.Fatal("build guidance does not match enabled tools")
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 1 || resources.NextCursor != "" {
		t.Fatalf("resources/list: %+v, %v", resources, err)
	}
	resource := resources.Resources[0]
	if resource.URI != "artifactory://instructions" || resource.Name != "usage_instructions" || resource.MIMEType != "text/markdown" || resource.Title == "" || resource.Description == "" || resource.Size != int64(len(guide)) {
		t.Fatalf("invalid instructions resource metadata: %+v", resource)
	}
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: resource.URI})
	if err != nil || len(result.Contents) != 1 {
		t.Fatalf("resources/read: %+v, %v", result, err)
	}
	content := result.Contents[0]
	if content.URI != resource.URI || content.MIMEType != resource.MIMEType || content.Text != guide || len(content.Blob) != 0 {
		t.Fatal("resource content differs from initialization instructions")
	}
	for _, uri := range []string{"artifactory://missing", "artifactory://instructions/other", "artifactory://instructions?extra=1", "file:///etc/passwd"} {
		_, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		var rpcError *jsonrpc.Error
		if !errors.As(err, &rpcError) || rpcError.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("unknown URI %s: expected resource-not-found error, got %v", uri, err)
		}
	}
}
