package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"artifactory-mcp/internal/artifactory"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type discoveryExecutor struct{ calls int }

func (e *discoveryExecutor) Execute(_ context.Context, op, _ string, _ []byte) ([]byte, error) {
	e.calls++
	if op == "build_runs" {
		return []byte(`{"buildsNumbers":[{"uri":"/42","started":"2026-01-01T12:00:00Z"}]}`), nil
	}
	return []byte(`{"results":[{"repo":"libs","path":".","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.version","value":"1.0"}]}]}`), nil
}

func TestDiscoveryToolProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	executor := &discoveryExecutor{}
	session := connectTestClient(t, ctx, New(artifactory.New(executor, []string{"libs"}), "test"))
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		arguments map[string]any
		field     string
	}{
		{"search_packages", map[string]any{"package_type": "npm", "name_pattern": "wid*"}, "packages"},
		{"list_package_versions", map[string]any{"package_type": "npm", "name": "widget"}, "versions"},
		{"list_build_runs", map[string]any{"name": "job"}, "runs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.arguments})
			if err != nil || result.IsError || result.StructuredContent == nil || len(result.Content) != 1 {
				t.Fatalf("tool call: %+v %v", result, err)
			}
			encoded, _ := json.Marshal(result.StructuredContent)
			var value map[string]any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
			if len(value[test.field].([]any)) != 1 {
				t.Fatal("missing discovery entry")
			}
			var textValue map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &textValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, textValue) {
				t.Fatal("structured and text content differ")
			}
			for _, tool := range listed.Tools {
				if tool.Name != test.name {
					continue
				}
				schemaJSON, _ := json.Marshal(tool.OutputSchema)
				var schema jsonschema.Schema
				if err := json.Unmarshal(schemaJSON, &schema); err != nil {
					t.Fatal(err)
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := resolved.Validate(value); err != nil {
					t.Fatalf("output does not match schema: %v", err)
				}
			}
		})
	}
	before := executor.calls
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"search_packages", map[string]any{}},
		{"search_packages", map[string]any{"package_type": "pypi"}},
		{"list_package_versions", map[string]any{"package_type": "npm"}},
		{"list_package_versions", map[string]any{"package_type": "npm", "name": "widget", "repositories": []string{"other"}}},
		{"list_build_runs", map[string]any{"name": 42}},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid call accepted: %+v %v", result, err)
		}
	}
	if executor.calls != before {
		t.Fatal("invalid or forbidden arguments reached executor")
	}
}
