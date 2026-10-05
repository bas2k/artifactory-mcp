package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"artifactory-mcp/internal/artifactory"

	"github.com/google/jsonschema-go/jsonschema"
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

func TestDisabledTools(t *testing.T) {
	all := []string{"list_repositories", "search_artifacts", "get_artifact_info", "list_folder", "get_artifact_properties", "get_artifact_stats", "list_builds", "get_build_info"}
	cases := [][]string{{"list_builds", "get_build_info"}, {"list_repositories", "list_repositories"}, all}
	for _, name := range all {
		cases = append(cases, []string{name})
	}
	for _, disabled := range cases {
		t.Run(strings.Join(disabled, ","), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reader := &fakeReader{}
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			serverSession, err := New(reader, "test", disabled...).Connect(ctx, serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
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
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(reader, "test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 8 {
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
}
