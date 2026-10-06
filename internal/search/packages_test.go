package search

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPackageQueryScopeAndLiterals(t *testing.T) {
	pattern := `@acme/wi").include("*").delete("true")*`
	query, limit, err := BuildPackages(PackageFilters{PackageType: "npm", NamePattern: pattern}, []string{"npm-local"}, false)
	if err != nil || limit != 100 {
		t.Fatalf("query: %s %v", query, err)
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(query, "items.find(")))
	var criteria map[string][]map[string]json.RawMessage
	if err := decoder.Decode(&criteria); err != nil {
		t.Fatal(err)
	}
	var gotPattern map[string]string
	json.Unmarshal(criteria["$and"][2]["@npm.name"], &gotPattern)
	if gotPattern["$match"] != pattern {
		t.Fatalf("literal changed: %+v", gotPattern)
	}
	if tail := query[len("items.find(")+int(decoder.InputOffset()):]; tail != `).include("repo","path","name","property.*")` {
		t.Fatalf("unexpected executable query suffix: %s", tail)
	}
	if !strings.Contains(query, `"repo":"npm-local"`) {
		t.Fatal("default repository scope missing")
	}
	query, _, err = BuildPackages(PackageFilters{PackageType: "npm", NamePattern: "@acme/widget"}, nil, true)
	if err != nil || !strings.Contains(query, `"$eq":"@acme/widget"`) {
		t.Fatalf("exact lookup: %s %v", query, err)
	}
	for _, kind := range []string{"npm", "nuget", "docker", "maven"} {
		query, _, err := BuildPackages(PackageFilters{PackageType: kind, Group: "", Limit: 1, Offset: 10}, nil, false)
		if err != nil || strings.Contains(query, ".limit(") || strings.Contains(query, ".offset(") || strings.Contains(query, ".sort(") || strings.Contains(query, ".transitive(") {
			t.Fatalf("unsupported package query modifiers for %s: %s %v", kind, query, err)
		}
	}
}

func TestPackageQueryOSSPropertyOutput(t *testing.T) {
	for _, kind := range []string{"npm", "nuget", "docker", "maven"} {
		group := ""
		if kind == "maven" {
			group = "org.example"
		}
		for _, exact := range []bool{false, true} {
			query, _, err := BuildPackages(PackageFilters{PackageType: kind, NamePattern: "widget", Group: group}, nil, exact)
			if err != nil {
				t.Fatal(err)
			}
			_, output, ok := strings.Cut(query, ").include(")
			if !ok || strings.Contains(output, "@") || (kind != "maven" && !strings.Contains(output, `"property.*"`)) {
				t.Fatalf("OSS-incompatible property output for %s (exact=%t): %s", kind, exact, query)
			}
		}
	}
}

func TestPackageQueryValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		filters PackageFilters
		exact   bool
	}{
		{"unsupported type", PackageFilters{PackageType: "pypi"}, false},
		{"scope", PackageFilters{PackageType: "npm", Repositories: []string{"other"}}, false},
		{"repository traversal", PackageFilters{PackageType: "npm", Repositories: []string{"../libs"}}, false},
		{"invalid limit", PackageFilters{PackageType: "npm", Limit: 501}, false},
		{"invalid offset", PackageFilters{PackageType: "npm", Offset: -1}, false},
		{"oversized name", PackageFilters{PackageType: "npm", NamePattern: strings.Repeat("x", 257)}, false},
		{"control", PackageFilters{PackageType: "npm", NamePattern: "a\n"}, false},
		{"URL", PackageFilters{PackageType: "npm", NamePattern: "https://example.test"}, true},
		{"traversal", PackageFilters{PackageType: "npm", NamePattern: "../pkg"}, true},
		{"wildcard exact", PackageFilters{PackageType: "npm", NamePattern: "pkg*"}, true},
		{"missing name", PackageFilters{PackageType: "npm"}, true},
		{"missing group", PackageFilters{PackageType: "maven", NamePattern: "widget"}, true},
		{"invalid group", PackageFilters{PackageType: "maven", Group: "org..example"}, false},
		{"group wildcard", PackageFilters{PackageType: "maven", Group: "org.*"}, false},
		{"wrong group ecosystem", PackageFilters{PackageType: "npm", Group: "org.example"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := BuildPackages(test.filters, []string{"libs"}, test.exact); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	repos := make([]string, 1000)
	for i := range repos {
		repos[i] = "long-repository-name"
	}
	if _, _, err := BuildPackages(PackageFilters{PackageType: "npm", Repositories: repos}, nil, false); err == nil {
		t.Fatal("oversized AQL accepted")
	}
}
