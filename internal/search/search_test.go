package search

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildEscapesAndScopes(t *testing.T) {
	attack := `foo"}).include("*").limit(99999)//`
	query, limit, err := Build(Filters{NamePattern: attack, Properties: map[string]string{"branch": attack}, Offset: 20}, []string{"libs", "仓库"})
	if err != nil {
		t.Fatal(err)
	}
	if limit != 100 || !strings.Contains(query, `.include("repo","path","name"`) || !strings.HasSuffix(query, `.offset(20).limit(100)`) {
		t.Fatalf("invalid query: %s", query)
	}
	end := strings.LastIndex(query, `).include(`)
	var body map[string]any
	if err := json.Unmarshal([]byte(query[len("items.find("):end]), &body); err != nil {
		t.Fatal("AQL filter JSON is invalid", err)
	}
	if !strings.Contains(query, `\"`) || !strings.Contains(query, `"$or":[{"repo":"libs"},{"repo":"仓库"}]`) {
		t.Fatalf("escaping or scope lost: %s", query)
	}
}
func TestInvalidFilters(t *testing.T) {
	negative := int64(-1)
	min, max := int64(20), int64(10)
	for _, f := range []Filters{{Repositories: []string{"other"}}, {Repositories: []string{"../libs"}}, {Limit: 501}, {Limit: -1}, {Offset: -1}, {CreatedAfter: "yesterday"}, {CreatedAfter: "2026-01-02T00:00:00Z", CreatedBefore: "2026-01-01T00:00:00Z"}, {MinSize: &negative}, {MinSize: &min, MaxSize: &max}, {NamePattern: strings.Repeat("x", 6000)}} {
		if _, _, err := Build(f, []string{"libs"}); err == nil {
			t.Errorf("accepted invalid filters %+v", f)
		}
	}
}
