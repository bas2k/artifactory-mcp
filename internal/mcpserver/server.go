package mcpserver

import (
	"context"
	"encoding/json"

	"artifactory-mcp/internal/artifactory"

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
	instructions := renderInstructions(disabledTools)
	server := mcp.NewServer(&mcp.Implementation{Name: "artifactory-mcp", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	addInstructionsResource(server, instructions)
	add(server, "list_repositories", "Discover repositories visible to the token and configured allowlist.", reader.ListRepositories)
	add(server, "get_server_info", "Read Artifactory version, revision, installed add-ons, and server-reported license value. License is empty when absent.", func(ctx context.Context, _ struct{}) (artifactory.ServerInfo, error) {
		return reader.ServerInfo(ctx)
	})
	add(server, "search_artifacts", "Search indexed artifacts using typed AQL filters and bounded offset paging in upstream order. Compatible with Artifactory OSS; ordering is not guaranteed.", reader.Search)
	add(server, "search_artifacts_sorted", "Search indexed artifacts sorted ascending by repository, path, and name using typed AQL filters and bounded offset paging. Requires AQL sorting support; unavailable in Artifactory OSS.", reader.SearchSorted)
	add(server, "search_packages", "Find indexed npm, NuGet, Maven, or Docker package versions using names and repository scope. Maven requires standard Maven layout; Docker returns tags from manifests. Results group matching artifact locations and use local output paging after a bounded metadata fetch. No remote resolution or latest-version ordering.", reader.SearchPackages)
	add(server, "list_package_versions", "List indexed versions or Docker tags for an exact package name; Maven also requires group. Results sort lexically by repository, group, name, and version and use local output paging after a bounded metadata fetch. No latest-version semantics or remote resolution.", reader.ListPackageVersions)
	add(server, "get_artifact_info", "Read artifact size, timestamps, MIME type, and checksums.", reader.ArtifactInfo)
	add(server, "list_folder", "List immediate children using ordinary folder information.", reader.Folder)
	add(server, "get_artifact_properties", "Read artifact properties, optionally selecting keys after a bounded fetch.", reader.Properties)
	add(server, "get_artifact_stats", "Read artifact download statistics.", reader.Stats)
	add(server, "list_builds", "List builds with local output paging after a bounded upstream fetch. Build permissions are separate from repository scope.", reader.ListBuilds)
	add(server, "list_build_runs", "Discover run numbers and start times for a named build, newest first, with local output paging after a bounded upstream fetch. Pass number and started to get_build_info. Build permissions are separate from repository scope.", reader.ListBuildRuns)
	add(server, "get_build_info", "Read build identity, status, VCS, and paged modules/artifacts/dependencies. Environment variables and arbitrary properties are excluded.", reader.BuildInfo)
	server.RemoveTools(disabledTools...)
	return server
}
