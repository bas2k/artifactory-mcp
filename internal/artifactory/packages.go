package artifactory

import (
	"cmp"
	"context"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"

	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/search"
)

type PackageVersionsInput struct {
	PackageType  string   `json:"package_type" jsonschema:"Package ecosystem: npm, nuget, maven, or docker"`
	Repositories []string `json:"repositories,omitempty" jsonschema:"Exact local or cache repository keys; defaults to configured scope"`
	Name         string   `json:"name" jsonschema:"Exact package name, Maven artifact ID, or Docker image name"`
	Group        string   `json:"group,omitempty" jsonschema:"Maven group ID; required for maven, omitted for other ecosystems"`
	Limit        int      `json:"limit,omitempty" jsonschema:"Output page size from 1 to 500; defaults to 100; does not reduce upstream fetch"`
	Offset       int      `json:"offset,omitempty"`
}

type PackageVersion struct {
	Repository  string   `json:"repository"`
	PackageType string   `json:"package_type"`
	Name        string   `json:"name"`
	Group       string   `json:"group,omitempty"`
	Version     string   `json:"version" jsonschema:"Package version or Docker tag; no latest-version semantics"`
	Paths       []string `json:"paths" jsonschema:"Matching package artifacts; Maven POMs and Docker manifests are representative files"`
}

type Packages struct {
	Packages []PackageVersion `json:"packages"`
	Page     Page             `json:"page"`
	Range    *Range           `json:"upstream_range,omitempty"`
	Notices  []string         `json:"notices,omitempty"`
}

type PackageVersions struct {
	Versions []PackageVersion `json:"versions"`
	Page     Page             `json:"page"`
	Range    *Range           `json:"upstream_range,omitempty"`
	Notices  []string         `json:"notices,omitempty"`
}

type packageArtifact struct {
	Repo       string `json:"repo"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Properties []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"properties"`
}

func (a packageArtifact) property(key string) string {
	value := ""
	for _, p := range a.Properties {
		if p.Key == key {
			if value != "" && value != p.Value {
				return "" // Ambiguous metadata must not invent a package identity.
			}
			value = p.Value
		}
	}
	return value
}

func (a packageArtifact) packageVersion(kind string) (PackageVersion, bool) {
	p := PackageVersion{Repository: a.Repo, PackageType: kind}
	switch kind {
	case "npm":
		p.Name, p.Version = a.property("npm.name"), a.property("npm.version")
	case "nuget":
		p.Name, p.Version = a.property("nuget.id"), a.property("nuget.version")
	case "docker":
		p.Name = a.property("docker.repoName")
		prefix, tag, ok := strings.Cut(a.Path, p.Name+"/")
		if (a.Name != "manifest.json" && a.Name != "list.manifest.json") || p.Name == "" || !ok || prefix != "" || config.ValidateSegment(tag) != nil || dockerDigest(tag) {
			return p, false
		}
		p.Version = tag
	case "maven":
		parts := strings.Split(a.Path, "/")
		if len(parts) < 3 || !strings.HasSuffix(a.Name, ".pom") {
			return p, false
		}
		p.Group = strings.Join(parts[:len(parts)-2], ".")
		p.Name, p.Version = parts[len(parts)-2], parts[len(parts)-1]
		if !strings.HasPrefix(a.Name, p.Name+"-") {
			return p, false
		}
	}
	if p.Name == "" || p.Version == "" || config.ValidatePath(p.Name, false) != nil || config.ValidateSegment(p.Version) != nil {
		return p, false
	}
	path := a.Name
	if a.Path != "." {
		path = a.Path + "/" + a.Name
	}
	p.Paths = []string{path}
	return p, true
}

// AQL wildcard semantics include slashes and treat all other characters
// literally. Do not use filepath/path.Match, which gives [] and / meanings.
func packageNameMatcher(pattern string, exact bool) *regexp.Regexp {
	if pattern == "" {
		pattern = "*"
	}
	expression := regexp.QuoteMeta(pattern)
	if !exact {
		expression = strings.NewReplacer("\\*", ".*", "\\?", ".").Replace(expression)
	}
	return regexp.MustCompile("^" + expression + "$")
}

func dockerDigest(tag string) bool {
	for _, prefix := range []string{"sha256:", "sha256__"} {
		if strings.HasPrefix(tag, prefix) && len(tag) == len(prefix)+64 {
			_, err := hex.DecodeString(strings.TrimPrefix(tag, prefix))
			return err == nil
		}
	}
	return false
}

func (c *Client) SearchPackages(ctx context.Context, in search.PackageFilters) (Packages, error) {
	return c.packages(ctx, in, false)
}

func (c *Client) ListPackageVersions(ctx context.Context, in PackageVersionsInput) (PackageVersions, error) {
	result, err := c.packages(ctx, search.PackageFilters{PackageType: in.PackageType, Repositories: in.Repositories, NamePattern: in.Name, Group: in.Group, Limit: in.Limit, Offset: in.Offset}, true)
	return PackageVersions{Versions: result.Packages, Page: result.Page, Range: result.Range, Notices: result.Notices}, err
}

func (c *Client) packages(ctx context.Context, in search.PackageFilters, exact bool) (Packages, error) {
	out := Packages{Packages: []PackageVersion{}}
	for _, repo := range in.Repositories {
		if !config.Allowed(repo, c.allowlist) {
			return out, NewError("forbidden", "repository is outside the configured allowlist")
		}
	}
	query, limit, err := search.BuildPackages(in, c.allowlist, exact)
	if err != nil {
		return out, invalid(err)
	}
	var raw struct {
		Results []packageArtifact `json:"results"`
		Range   *Range            `json:"range"`
		Notices []string          `json:"notices"`
	}
	if err := c.read(ctx, "aql", "api/search/aql", []byte(query), &raw); err != nil {
		return out, err
	}
	if raw.Results == nil {
		return out, NewError("unavailable", "upstream omitted the package search results array")
	}
	if raw.Range != nil && (raw.Range.Notification != "" || raw.Range.StartPos != 0 || raw.Range.Total > int64(len(raw.Results))) {
		return out, NewError("unavailable", "upstream truncated the package search; narrow the repository or name filters")
	}
	matcher := packageNameMatcher(in.NamePattern, exact)
	type identity struct{ repo, group, name, version string }
	grouped := map[identity]*PackageVersion{}
	skipped := false
	for _, artifact := range raw.Results {
		if config.ValidateSegment(artifact.Repo) != nil || config.ValidateSegment(artifact.Name) != nil {
			return out, NewError("unavailable", "upstream returned an invalid package artifact identity")
		}
		if !config.Allowed(artifact.Repo, c.allowlist) || !config.Allowed(artifact.Repo, in.Repositories) {
			return out, NewError("forbidden", "upstream returned a repository outside configured scope")
		}
		if in.PackageType == "docker" {
			if slash := strings.LastIndex(artifact.Path, "/"); slash > 0 && dockerDigest(artifact.Path[slash+1:]) && config.ValidatePath(artifact.Path[:slash], false) == nil {
				// Multi-architecture child manifests are indexed under their
				// digest. They are not user-facing tags.
				continue
			}
		}
		if artifact.Path != "." && config.ValidatePath(artifact.Path, false) != nil {
			return out, NewError("unavailable", "upstream returned an invalid package artifact identity")
		}
		p, ok := artifact.packageVersion(in.PackageType)
		if !ok {
			skipped = true
			continue
		}
		if !matcher.MatchString(p.Name) || (in.Group != "" && p.Group != in.Group) {
			continue
		}
		key := identity{p.Repository, p.Group, p.Name, p.Version}
		if existing := grouped[key]; existing != nil {
			existing.Paths = append(existing.Paths, p.Paths...)
		} else {
			grouped[key] = &p
		}
	}
	for _, p := range grouped {
		slices.Sort(p.Paths)
		p.Paths = slices.Compact(p.Paths)
		out.Packages = append(out.Packages, *p)
	}
	slices.SortFunc(out.Packages, func(a, b PackageVersion) int {
		return cmp.Or(
			cmp.Compare(a.Repository, b.Repository),
			cmp.Compare(a.Group, b.Group),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Version, b.Version),
		)
	})
	out.Packages, out.Page = localPage(out.Packages, limit, in.Offset)
	out.Range, out.Notices = raw.Range, raw.Notices
	if skipped {
		out.Notices = append(out.Notices, "Some artifacts lack unambiguous supported package metadata or layout and were omitted.")
	}
	return out, nil
}
