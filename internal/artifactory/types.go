package artifactory

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"artifactory-mcp/internal/search"
)

type RepositoryFilter struct {
	Type        string `json:"type,omitempty"`
	PackageType string `json:"package_type,omitempty"`
	Project     string `json:"project,omitempty"`
}
type Repository struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	PackageType string `json:"packageType"`
	Description string `json:"description,omitempty"`
}
type Repositories struct {
	Repositories []Repository `json:"repositories"`
}

type ServerInfo struct {
	Version  string   `json:"version"`
	Revision string   `json:"revision,omitempty"`
	License  string   `json:"license" jsonschema:"Server-reported license value; empty when absent"`
	Addons   []string `json:"addons"`
}
type ArtifactInput struct {
	Repository string `json:"repository"`
	Path       string `json:"path" jsonschema:"Relative artifact or folder path; empty path selects repository root for list_folder"`
}
type PropertiesInput struct {
	Repository string   `json:"repository"`
	Path       string   `json:"path"`
	Keys       []string `json:"keys,omitempty"`
}
type Checksums struct {
	SHA1   string `json:"sha1,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	MD5    string `json:"md5,omitempty"`
}
type ArtifactInfo struct {
	Repo         string    `json:"repo"`
	Path         string    `json:"path"`
	Created      string    `json:"created,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	LastUpdated  string    `json:"lastUpdated,omitempty"`
	Size         string    `json:"size,omitempty" jsonschema:"Exact decimal byte count"`
	MimeType     string    `json:"mimeType,omitempty"`
	Checksums    Checksums `json:"checksums"`
}
type Child struct {
	URI    string `json:"uri"`
	Folder bool   `json:"folder"`
}
type Folder struct {
	Repo     string  `json:"repo"`
	Path     string  `json:"path"`
	Children []Child `json:"children"`
}
type Properties struct {
	Properties map[string][]string `json:"properties"`
}
type Stats struct {
	DownloadCount        int64  `json:"downloadCount"`
	LastDownloaded       int64  `json:"lastDownloaded"`
	LastDownloadedBy     string `json:"lastDownloadedBy,omitempty"`
	RemoteDownloadCount  int64  `json:"remoteDownloadCount,omitempty"`
	RemoteLastDownloaded int64  `json:"remoteLastDownloaded,omitempty"`
}
type Artifact struct {
	Repo     string `json:"repo"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Created  string `json:"created,omitempty"`
	Modified string `json:"modified,omitempty"`
	SHA1     string `json:"actual_sha1,omitempty"`
	MD5      string `json:"actual_md5,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}
type Page struct {
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
	Returned int    `json:"returned"`
	HasMore  bool   `json:"has_more"`
	Paging   string `json:"paging" jsonschema:"upstream or local_output"`
	Total    *int64 `json:"total,omitempty" jsonschema:"Count from a bounded locally fetched collection; omitted for upstream artifact paging"`
}
type SearchResult struct {
	Artifacts []Artifact `json:"artifacts"`
	Page      Page       `json:"page"`
	Range     *Range     `json:"upstream_range,omitempty"`
	Notices   []string   `json:"notices,omitempty"`
}
type Range struct {
	StartPos     int64  `json:"start_pos"`
	EndPos       int64  `json:"end_pos"`
	Total        int64  `json:"total"`
	Notification string `json:"notification,omitempty"`
}
type BuildsInput struct {
	Project    string `json:"project,omitempty"`
	NameFilter string `json:"name_filter,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}
type BuildSummary struct {
	Name        string `json:"name"`
	LastStarted string `json:"last_started,omitempty"`
}
type Builds struct {
	Builds []BuildSummary `json:"builds"`
	Page   Page           `json:"page"`
}
type BuildInput struct {
	Name         string `json:"name"`
	Number       string `json:"number"`
	Project      string `json:"project,omitempty"`
	Started      string `json:"started,omitempty"`
	ModuleLimit  int    `json:"module_limit,omitempty"`
	ModuleOffset int    `json:"module_offset,omitempty"`
	DetailLimit  int    `json:"detail_limit,omitempty"`
	DetailOffset int    `json:"detail_offset,omitempty"`
}
type BuildFile struct {
	Name   string `json:"name,omitempty"`
	ID     string `json:"id,omitempty"`
	Type   string `json:"type,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	MD5    string `json:"md5,omitempty"`
}
type VCS struct {
	URL      string `json:"url,omitempty"`
	Revision string `json:"revision,omitempty"`
	Branch   string `json:"branch,omitempty"`
}
type BuildStatus struct {
	Status    string `json:"status,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}
type Module struct {
	ID               string      `json:"id"`
	Artifacts        []BuildFile `json:"artifacts"`
	Dependencies     []BuildFile `json:"dependencies"`
	ArtifactsPage    Page        `json:"artifacts_page"`
	DependenciesPage Page        `json:"dependencies_page"`
}
type BuildInfo struct {
	Name           string        `json:"name"`
	Number         string        `json:"number"`
	Started        string        `json:"started,omitempty"`
	DurationMillis int64         `json:"durationMillis,omitempty"`
	Statuses       []BuildStatus `json:"statuses,omitempty"`
	VCS            []VCS         `json:"vcs,omitempty"`
	Modules        []Module      `json:"modules"`
	ModulesPage    Page          `json:"modules_page"`
}

type Reader interface {
	ServerInfo(context.Context) (ServerInfo, error)
	ListRepositories(context.Context, RepositoryFilter) (Repositories, error)
	Search(context.Context, search.Filters) (SearchResult, error)
	SearchSorted(context.Context, search.Filters) (SearchResult, error)
	SearchPackages(context.Context, search.PackageFilters) (Packages, error)
	ListPackageVersions(context.Context, PackageVersionsInput) (PackageVersions, error)
	ArtifactInfo(context.Context, ArtifactInput) (ArtifactInfo, error)
	Folder(context.Context, ArtifactInput) (Folder, error)
	Properties(context.Context, PropertiesInput) (Properties, error)
	Stats(context.Context, ArtifactInput) (Stats, error)
	ListBuilds(context.Context, BuildsInput) (Builds, error)
	ListBuildRuns(context.Context, BuildRunsInput) (BuildRuns, error)
	BuildInfo(context.Context, BuildInput) (BuildInfo, error)
}

// Executor is internal infrastructure, never exposed as an MCP tool.
type Executor interface {
	Execute(context.Context, string, string, []byte) ([]byte, error)
}

// Decode rejects trailing data as well as malformed JSON.
func Decode(data []byte, v any) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return NewError("unavailable", "upstream returned an empty JSON value")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return NewError("unavailable", "upstream returned malformed JSON")
	}
	return nil
}

// RedactJSON preserves decimal number tokens while removing the configured
// credential from every returned string, including escaped strings and keys.
func RedactJSON(data []byte, secret string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || !json.Valid(data) {
		return nil, NewError("unavailable", "upstream returned malformed JSON")
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return strings.ReplaceAll(x, secret, "[REDACTED]")
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, v := range x {
				out[strings.ReplaceAll(k, secret, "[REDACTED]")] = walk(v)
			}
			return out
		default:
			return v
		}
	}
	if secret != "" {
		value = walk(value)
	}
	return json.Marshal(value)
}
