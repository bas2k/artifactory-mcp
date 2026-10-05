package mcpserver

import (
	"context"
	"encoding/json"

	"artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/search"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func failure(err error) *mcp.CallToolResult {
	data, _ := json.Marshal(artifactory.Classify(err))
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}
func add[In, Out any](server *mcp.Server, name, description string, handler func(context.Context, In) (Out, error)) {
	input, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	}
	output, err := jsonschema.For[Out](nil)
	if err != nil {
		panic(err)
	}
	resolved, err := input.Resolve(nil)
	if err != nil {
		panic(err)
	}
	server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: input, OutputSchema: output, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		data := req.Params.Arguments
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return failure(artifactory.NewError("invalid_input", "arguments must be a JSON object")), nil
		}
		if err := resolved.Validate(value); err != nil {
			return failure(artifactory.NewError("invalid_input", "arguments do not match the tool input schema")), nil
		}
		var in In
		if err := json.Unmarshal(data, &in); err != nil {
			return failure(artifactory.NewError("invalid_input", "arguments cannot be decoded")), nil
		}
		out, err := handler(ctx, in)
		if err != nil {
			return failure(err), nil
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return failure(artifactory.NewError("unavailable", "cannot encode tool output")), nil
		}
		return &mcp.CallToolResult{StructuredContent: json.RawMessage(encoded), Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
}
func New(reader artifactory.Reader, version string, disabledTools ...string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "artifactory-mcp", Version: version}, nil)
	add(server, "list_repositories", "Discover repositories visible to the token and configured allowlist.", reader.ListRepositories)
	add(server, "search_artifacts", "Search indexed artifacts using typed AQL filters and bounded offset paging.", func(ctx context.Context, in search.Filters) (artifactory.SearchResult, error) {
		return reader.Search(ctx, in)
	})
	add(server, "get_artifact_info", "Read artifact size, timestamps, MIME type, and checksums.", reader.ArtifactInfo)
	add(server, "list_folder", "List immediate children using ordinary folder information.", reader.Folder)
	add(server, "get_artifact_properties", "Read artifact properties, optionally selecting keys after a bounded fetch.", reader.Properties)
	add(server, "get_artifact_stats", "Read artifact download statistics.", reader.Stats)
	add(server, "list_builds", "List builds with local output paging after a bounded upstream fetch. Build permissions are separate from repository scope.", reader.ListBuilds)
	add(server, "get_build_info", "Read build identity, status, VCS, and paged modules/artifacts/dependencies. Environment variables and arbitrary properties are excluded.", reader.BuildInfo)
	server.RemoveTools(disabledTools...)
	return server
}
