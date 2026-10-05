package artifactory

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/search"
)

type Client struct {
	executor  Executor
	allowlist []string
}

func New(executor Executor, allowlist []string) *Client {
	return &Client{executor, append([]string(nil), allowlist...)}
}
func (c *Client) read(ctx context.Context, op, path string, body []byte, out any) error {
	data, err := c.executor.Execute(ctx, op, path, body)
	if err != nil {
		return Classify(err)
	}
	return Decode(data, out)
}
func invalid(err error) error { return NewError("invalid_input", err.Error()) }

func (c *Client) ServerInfo(ctx context.Context) (ServerInfo, error) {
	var raw struct {
		Version  string   `json:"version"`
		Revision string   `json:"revision"`
		License  string   `json:"license"`
		Addons   []string `json:"addons"`
	}
	if err := c.read(ctx, "version", "api/system/version", nil, &raw); err != nil {
		return ServerInfo{}, err
	}
	if strings.TrimSpace(raw.Version) == "" {
		return ServerInfo{}, NewError("unavailable", "upstream omitted the Artifactory version")
	}

	return ServerInfo{Version: raw.Version, Revision: raw.Revision, License: raw.License, Addons: append([]string{}, raw.Addons...)}, nil
}

func (c *Client) storage(in ArtifactInput, empty bool) (string, error) {
	if config.ValidateSegment(in.Repository) != nil || config.ValidatePath(in.Path, empty) != nil {
		return "", NewError("invalid_input", "repository and path must be relative names without traversal or URL components")
	}
	if !config.Allowed(in.Repository, c.allowlist) {
		return "", NewError("forbidden", "repository is outside the configured allowlist")
	}
	parts := []string{in.Repository}
	if in.Path != "" {
		parts = append(parts, strings.Split(in.Path, "/")...)
	}
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "api/storage/" + strings.Join(parts, "/"), nil
}
func projectQuery(project string) (url.Values, error) {
	q := url.Values{}
	if project != "" {
		if config.ValidateSegment(project) != nil {
			return nil, NewError("invalid_input", "invalid project key")
		}
		q.Set("project", project)
	}
	return q, nil
}
func queryPath(path string, q url.Values) string {
	if len(q) > 0 {
		return path + "?" + q.Encode()
	}
	return path
}
func (c *Client) ListRepositories(ctx context.Context, in RepositoryFilter) (Repositories, error) {
	out := Repositories{Repositories: []Repository{}}
	q, err := projectQuery(in.Project)
	if err != nil {
		return out, err
	}
	if in.Type != "" {
		switch strings.ToLower(in.Type) {
		case "local", "remote", "virtual", "federated":
			q.Set("type", strings.ToLower(in.Type))
		default:
			return out, NewError("invalid_input", "unsupported repository type")
		}
	}
	if in.PackageType != "" {
		if config.ValidateSegment(in.PackageType) != nil {
			return out, NewError("invalid_input", "invalid package type")
		}
		q.Set("packageType", in.PackageType)
	}
	var repos []Repository
	if err := c.read(ctx, "repositories", queryPath("api/repositories", q), nil, &repos); err != nil {
		return out, err
	}
	for _, repo := range repos {
		if config.Allowed(repo.Key, c.allowlist) {
			out.Repositories = append(out.Repositories, repo)
		}
	}
	sort.Slice(out.Repositories, func(i, j int) bool { return out.Repositories[i].Key < out.Repositories[j].Key })
	return out, nil
}
func (c *Client) Search(ctx context.Context, in search.Filters) (SearchResult, error) {
	return c.search(ctx, in, false)
}

func (c *Client) SearchSorted(ctx context.Context, in search.Filters) (SearchResult, error) {
	return c.search(ctx, in, true)
}

func (c *Client) search(ctx context.Context, in search.Filters, sorted bool) (SearchResult, error) {
	out := SearchResult{Artifacts: []Artifact{}}
	for _, r := range in.Repositories {
		if !config.Allowed(r, c.allowlist) {
			return out, NewError("forbidden", "repository is outside the configured allowlist")
		}
	}
	build := search.Build
	if sorted {
		build = search.BuildSorted
	}
	query, limit, err := build(in, c.allowlist)
	if err != nil {
		return out, invalid(err)
	}
	var raw struct {
		Results []Artifact `json:"results"`
		Range   *Range     `json:"range"`
		Notices []string   `json:"notices"`
	}
	if err := c.read(ctx, "aql", "api/search/aql", []byte(query), &raw); err != nil {
		return out, err
	}
	if raw.Results == nil {
		return out, NewError("unavailable", "upstream omitted the search results array")
	}
	if len(raw.Results) > limit {
		return out, NewError("unavailable", "upstream ignored the requested search limit")
	}
	for _, artifact := range raw.Results {
		if artifact.Repo == "" || artifact.Path == "" || artifact.Name == "" {
			return out, NewError("unavailable", "upstream omitted required artifact identity fields")
		}
		if !config.Allowed(artifact.Repo, c.allowlist) {
			return out, NewError("forbidden", "upstream returned a repository outside configured scope")
		}
	}
	if raw.Results != nil {
		out.Artifacts = raw.Results
	}
	out.Range = raw.Range
	out.Notices = raw.Notices
	out.Page = Page{Limit: limit, Offset: in.Offset, Returned: len(out.Artifacts), HasMore: len(out.Artifacts) == limit, Paging: "upstream"}
	return out, nil
}
func (c *Client) ArtifactInfo(ctx context.Context, in ArtifactInput) (ArtifactInfo, error) {
	var out ArtifactInfo
	path, err := c.storage(in, false)
	if err != nil {
		return out, err
	}
	err = c.read(ctx, "artifact", path, nil, &out)
	if err == nil && (out.Repo == "" || out.Path == "") {
		err = NewError("unavailable", "upstream omitted artifact identity fields")
	}
	return out, err
}
func (c *Client) Folder(ctx context.Context, in ArtifactInput) (Folder, error) {
	var out Folder
	path, err := c.storage(in, true)
	if err != nil {
		return out, err
	}
	err = c.read(ctx, "folder", path, nil, &out)
	if err == nil && (out.Repo == "" || out.Path == "" || out.Children == nil) {
		err = NewError("unavailable", "upstream did not return folder information")
	}
	return out, err
}
func (c *Client) Properties(ctx context.Context, in PropertiesInput) (Properties, error) {
	var out Properties
	path, err := c.storage(ArtifactInput{in.Repository, in.Path}, false)
	if err != nil {
		return out, err
	}
	for _, k := range in.Keys {
		if k == "" || strings.ContainsAny(k, "\r\n\x00") {
			return out, NewError("invalid_input", "invalid property key")
		}
	}
	// The SDK exposes only the complete properties endpoint. Slice keys locally
	// after a bounded fetch, retaining property names containing commas.
	err = c.read(ctx, "properties", path+"?properties", nil, &out)
	if err != nil {
		return out, err
	}
	if out.Properties == nil {
		return out, NewError("unavailable", "upstream omitted the properties object")
	}
	if len(in.Keys) > 0 {
		filtered := map[string][]string{}
		for _, key := range in.Keys {
			if value, ok := out.Properties[key]; ok {
				filtered[key] = value
			}
		}
		out.Properties = filtered
	}
	return out, nil
}
func (c *Client) Stats(ctx context.Context, in ArtifactInput) (Stats, error) {
	var out Stats
	path, err := c.storage(in, false)
	if err != nil {
		return out, err
	}
	err = c.read(ctx, "stats", path+"?stats", nil, &out)
	return out, err
}
func localPage[T any](items []T, limit, offset int) ([]T, Page) {
	total := int64(len(items))
	start := min(offset, len(items))
	end := start + min(limit, len(items)-start)
	selected := append([]T{}, items[start:end]...)
	return selected, Page{Limit: limit, Offset: offset, Returned: len(selected), HasMore: end < len(items), Paging: "local_output", Total: &total}
}
func (c *Client) ListBuilds(ctx context.Context, in BuildsInput) (Builds, error) {
	out := Builds{Builds: []BuildSummary{}}
	limit, err := search.Paging(in.Limit, in.Offset)
	if err != nil {
		return out, invalid(err)
	}
	q, err := projectQuery(in.Project)
	if err != nil {
		return out, err
	}
	var raw struct {
		Builds []struct {
			URI         string `json:"uri"`
			LastStarted string `json:"lastStarted"`
		} `json:"builds"`
	}
	if err := c.read(ctx, "builds", queryPath("api/build", q), nil, &raw); err != nil {
		return out, err
	}
	if raw.Builds == nil {
		return out, NewError("unavailable", "upstream omitted the builds array")
	}
	for _, b := range raw.Builds {
		name := strings.TrimPrefix(b.URI, "/")
		if strings.Contains(name, "://") || strings.Contains(name, "/") {
			return out, NewError("unavailable", "upstream returned an invalid build name")
		}
		name, err = url.PathUnescape(name)
		if err != nil {
			return out, NewError("unavailable", "upstream returned an invalid build name")
		}
		if config.ValidateSegment(name) != nil {
			return out, NewError("unavailable", "upstream returned an invalid build name")
		}
		if strings.Contains(name, in.NameFilter) {
			out.Builds = append(out.Builds, BuildSummary{name, b.LastStarted})
		}
	}
	sort.Slice(out.Builds, func(i, j int) bool { return out.Builds[i].Name < out.Builds[j].Name })
	out.Builds, out.Page = localPage(out.Builds, limit, in.Offset)
	return out, nil
}
func (c *Client) BuildInfo(ctx context.Context, in BuildInput) (BuildInfo, error) {
	out := BuildInfo{Modules: []Module{}}
	if config.ValidateSegment(in.Name) != nil || config.ValidateSegment(in.Number) != nil {
		return out, NewError("invalid_input", "invalid build name or number")
	}
	ml, err := search.Paging(in.ModuleLimit, in.ModuleOffset)
	if err != nil {
		return out, invalid(err)
	}
	dl, err := search.Paging(in.DetailLimit, in.DetailOffset)
	if err != nil {
		return out, invalid(err)
	}
	q, err := projectQuery(in.Project)
	if err != nil {
		return out, err
	}
	if in.Started != "" {
		// Artifactory build timestamps include a numeric zone without a colon.
		if _, err := time.Parse("2006-01-02T15:04:05.000-0700", in.Started); err != nil {
			if _, err = time.Parse(time.RFC3339Nano, in.Started); err != nil {
				return out, NewError("invalid_input", "started must be an ISO8601 timestamp")
			}
		}
		q.Set("started", in.Started)
	}
	var raw struct {
		BuildInfo BuildInfo `json:"buildInfo"`
	}
	path := fmt.Sprintf("api/build/%s/%s", url.PathEscape(in.Name), url.PathEscape(in.Number))
	if err := c.read(ctx, "build", queryPath(path, q), nil, &raw); err != nil {
		return out, err
	}
	if raw.BuildInfo.Name == "" || raw.BuildInfo.Number == "" {
		return out, NewError("unavailable", "upstream omitted build identity fields")
	}
	out = raw.BuildInfo
	out.Modules, out.ModulesPage = localPage(out.Modules, ml, in.ModuleOffset)
	for i := range out.Modules {
		m := &out.Modules[i]
		m.Artifacts, m.ArtifactsPage = localPage(m.Artifacts, dl, in.DetailOffset)
		m.Dependencies, m.DependenciesPage = localPage(m.Dependencies, dl, in.DetailOffset)
	}
	return out, nil
}

func (c *Client) Close() error {
	if closer, ok := c.executor.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
