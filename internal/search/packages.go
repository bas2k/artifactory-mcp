package search

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"artifactory-mcp/internal/config"
)

type PackageFilters struct {
	PackageType  string   `json:"package_type" jsonschema:"Package ecosystem: npm, nuget, maven, or docker"`
	Repositories []string `json:"repositories,omitempty" jsonschema:"Exact local or cache repository keys; defaults to configured scope"`
	NamePattern  string   `json:"name_pattern,omitempty" jsonschema:"Case-sensitive package name pattern with * and ? wildcards; defaults to *"`
	Group        string   `json:"group,omitempty" jsonschema:"Exact Maven group ID; only valid for maven"`
	Limit        int      `json:"limit,omitempty" jsonschema:"Output page size from 1 to 500; defaults to 100; does not reduce upstream fetch"`
	Offset       int      `json:"offset,omitempty"`
}

// BuildPackages deliberately does not use AQL limit/offset: those modifiers
// cannot bound queries that include property fields. The executor bounds bytes
// and time, and the domain client groups and pages the fetched collection.
func BuildPackages(f PackageFilters, allowlist []string, exact bool) (string, int, error) {
	limit, err := Paging(f.Limit, f.Offset)
	if err != nil {
		return "", 0, err
	}
	pattern := f.NamePattern
	if pattern == "" {
		if exact {
			return "", 0, fmt.Errorf("package name is required")
		}
		pattern = "*"
	}
	if utf8.RuneCountInString(pattern) > 256 || strings.IndexFunc(pattern, unicode.IsControl) >= 0 || strings.ContainsAny(pattern, "\\%") || strings.Contains(pattern, "://") {
		return "", 0, fmt.Errorf("invalid package name or pattern")
	}
	if exact && (strings.ContainsAny(pattern, "*?") || config.ValidatePath(pattern, false) != nil) {
		return "", 0, fmt.Errorf("package name must be a relative name without wildcards")
	}
	if f.Group != "" {
		if f.PackageType != "maven" {
			return "", 0, fmt.Errorf("group is only supported for maven")
		}
		for _, part := range strings.Split(f.Group, ".") {
			if config.ValidateSegment(part) != nil || strings.ContainsAny(part, "*?") {
				return "", 0, fmt.Errorf("invalid Maven group ID")
			}
		}
	}
	repos := f.Repositories
	if len(repos) == 0 {
		repos = allowlist
	}
	clauses := []any{map[string]string{"type": "file"}}
	if len(repos) > 0 {
		choices := make([]any, 0, len(repos))
		for _, repo := range repos {
			if config.ValidateSegment(repo) != nil || !config.Allowed(repo, allowlist) {
				return "", 0, fmt.Errorf("invalid or disallowed repository key")
			}
			choices = append(choices, map[string]string{"repo": repo})
		}
		clauses = append(clauses, map[string]any{"$or": choices})
	}
	include := []string{"repo", "path", "name"}
	op := "$match"
	if exact {
		op = "$eq"
	}
	switch f.PackageType {
	case "npm", "nuget":
		nameKey := "npm.name"
		if f.PackageType == "nuget" {
			nameKey = "nuget.id"
		}
		clauses = append(clauses, map[string]any{"@" + nameKey: map[string]string{op: pattern}})
		// OSS rejects selecting individual property keys in include. Fetch
		// all properties and extract only package identity fields locally.
		include = append(include, "property.*")
	case "docker":
		clauses = append(clauses, map[string]any{"$or": []any{map[string]string{"name": "manifest.json"}, map[string]string{"name": "list.manifest.json"}}}, map[string]any{"@docker.repoName": map[string]string{op: pattern}})
		include = append(include, "property.*")
	case "maven":
		if strings.Contains(pattern, "/") || (exact && f.Group == "") {
			return "", 0, fmt.Errorf("package names for Maven are artifact IDs; group is required for version listing")
		}
		groupPath := "*"
		if f.Group != "" {
			groupPath = strings.ReplaceAll(f.Group, ".", "/")
		}
		clauses = append(clauses, map[string]any{"name": map[string]string{"$match": pattern + "-*.pom"}}, map[string]any{"path": map[string]string{"$match": groupPath + "/" + pattern + "/*"}})
	default:
		return "", 0, fmt.Errorf("package_type must be npm, nuget, maven, or docker")
	}
	criteria, _ := json.Marshal(map[string]any{"$and": clauses})
	fields := make([]string, len(include))
	for i, field := range include {
		encoded, _ := json.Marshal(field)
		fields[i] = string(encoded)
	}
	query := "items.find(" + string(criteria) + ").include(" + strings.Join(fields, ",") + ")"
	if utf8.RuneCountInString(query) > 6000 {
		return "", 0, fmt.Errorf("generated AQL exceeds 6000 characters")
	}
	return query, limit, nil
}
