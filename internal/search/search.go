package search

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"artifactory-mcp/internal/config"
)

type Filters struct {
	Repositories   []string          `json:"repositories,omitempty" jsonschema:"Exact repository keys; defaults to configured scope"`
	NamePattern    string            `json:"name_pattern,omitempty"`
	PathPattern    string            `json:"path_pattern,omitempty"`
	Properties     map[string]string `json:"properties,omitempty" jsonschema:"Property key to exact value filters"`
	CreatedAfter   string            `json:"created_after,omitempty" jsonschema:"RFC3339 timestamp"`
	CreatedBefore  string            `json:"created_before,omitempty" jsonschema:"RFC3339 timestamp"`
	ModifiedAfter  string            `json:"modified_after,omitempty" jsonschema:"RFC3339 timestamp"`
	ModifiedBefore string            `json:"modified_before,omitempty" jsonschema:"RFC3339 timestamp"`
	MinSize        *int64            `json:"min_size,omitempty"`
	MaxSize        *int64            `json:"max_size,omitempty"`
	Limit          int               `json:"limit,omitempty" jsonschema:"Page size from 1 to 500; defaults to 100"`
	Offset         int               `json:"offset,omitempty"`
}

func Paging(limit, offset int) (int, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 || offset < 0 || offset > int(^uint(0)>>1)-limit {
		return 0, fmt.Errorf("limit must be 1..500 and offset must be nonnegative and bounded")
	}
	return limit, nil
}

func Build(f Filters, allowlist []string) (string, int, error) {
	limit, err := Paging(f.Limit, f.Offset)
	if err != nil {
		return "", 0, err
	}
	repos := f.Repositories
	for _, r := range repos {
		if config.ValidateSegment(r) != nil {
			return "", 0, fmt.Errorf("invalid repository key")
		}
		if !config.Allowed(r, allowlist) {
			return "", 0, fmt.Errorf("repository is outside the configured allowlist")
		}
	}
	if len(repos) == 0 {
		repos = allowlist
	}
	clauses := []any{map[string]any{"type": "file"}}
	if len(repos) > 0 {
		choices := make([]any, 0, len(repos))
		for _, repo := range repos {
			choices = append(choices, map[string]string{"repo": repo})
		}
		clauses = append(clauses, map[string]any{"$or": choices})
	}
	for _, pattern := range []struct{ key, value string }{{"name", f.NamePattern}, {"path", f.PathPattern}} {
		if pattern.value != "" {
			clauses = append(clauses, map[string]any{pattern.key: map[string]string{"$match": pattern.value}})
		}
	}
	// JSON serialization keeps quotes and AQL metacharacters inside literal values.
	keys := make([]string, 0, len(f.Properties))
	for key := range f.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := f.Properties[k]
		if k == "" || len(k) > 256 || strings.ContainsAny(k, "\r\n\x00") {
			return "", 0, fmt.Errorf("invalid property key")
		}
		clauses = append(clauses, map[string]any{"@" + k: map[string]string{"$eq": v}})
	}
	for _, d := range []struct{ field, op, value string }{{"created", "$gt", f.CreatedAfter}, {"created", "$lt", f.CreatedBefore}, {"modified", "$gt", f.ModifiedAfter}, {"modified", "$lt", f.ModifiedBefore}} {
		if d.value != "" {
			if _, err := time.Parse(time.RFC3339, d.value); err != nil {
				return "", 0, fmt.Errorf("dates must be RFC3339 timestamps")
			}
			clauses = append(clauses, map[string]any{d.field: map[string]string{d.op: d.value}})
		}
	}
	for _, pair := range [][2]string{{f.CreatedAfter, f.CreatedBefore}, {f.ModifiedAfter, f.ModifiedBefore}} {
		if pair[0] != "" && pair[1] != "" {
			a, _ := time.Parse(time.RFC3339, pair[0])
			b, _ := time.Parse(time.RFC3339, pair[1])
			if !a.Before(b) {
				return "", 0, fmt.Errorf("date bounds must be increasing")
			}
		}
	}
	if f.MinSize != nil && f.MaxSize != nil && *f.MinSize > *f.MaxSize {
		return "", 0, fmt.Errorf("size bounds must be increasing")
	}
	for _, s := range []struct {
		op    string
		value *int64
	}{{"$gte", f.MinSize}, {"$lte", f.MaxSize}} {
		if s.value != nil {
			if *s.value < 0 {
				return "", 0, fmt.Errorf("sizes must be nonnegative")
			}
			clauses = append(clauses, map[string]any{"size": map[string]int64{s.op: *s.value}})
		}
	}
	data, err := json.Marshal(map[string]any{"$and": clauses})
	if err != nil {
		return "", 0, err
	}
	query := `items.find(` + string(data) + `).include("repo","path","name","size","created","modified","actual_sha1","actual_md5","sha256").sort({"$asc":["repo","path","name"]}).offset(` + strconv.Itoa(f.Offset) + `).limit(` + strconv.Itoa(limit) + `)`
	if utf8.RuneCountInString(query) > 6000 {
		return "", 0, fmt.Errorf("generated AQL exceeds 6000 characters")
	}
	return query, limit, nil
}
