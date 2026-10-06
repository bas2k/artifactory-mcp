package artifactory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"artifactory-mcp/internal/search"
)

func TestPackageEcosystems(t *testing.T) {
	for _, test := range []struct{ kind, name, group, version, results string }{
		{"npm", "@acme/widget", "", "1.0.0", `[{"repo":"libs","path":"@acme/widget/-","name":"widget-1.0.0.tgz","properties":[{"key":"npm.name","value":"@acme/widget"},{"key":"npm.version","value":"1.0.0"},{"key":"env.secret","value":"do-not-return"}]}]`},
		{"nuget", "Acme.Widget", "", "2.0.0", `[{"repo":"libs","path":".","name":"Acme.Widget.2.0.0.nupkg","properties":[{"key":"nuget.id","value":"Acme.Widget"},{"key":"nuget.version","value":"2.0.0"}]}]`},
		{"maven", "widget", "org.example", "1.0-SNAPSHOT", `[{"repo":"libs","path":"org/example/widget/1.0-SNAPSHOT","name":"widget-1.0-20260101.120000-1.pom"}]`},
		{"docker", "acme/widget", "", "stable", `[{"repo":"libs","path":"acme/widget/stable","name":"manifest.json","properties":[{"key":"docker.repoName","value":"acme/widget"}]}]`},
	} {
		t.Run(test.kind, func(t *testing.T) {
			calls := 0
			c := New(executeFunc(func(_ context.Context, op, path string, body []byte) ([]byte, error) {
				calls++
				if op != "aql" || path != "api/search/aql" || !strings.Contains(string(body), `"repo":"libs"`) {
					t.Fatalf("unexpected request: %s %s %s", op, path, body)
				}
				return []byte(`{"results":` + test.results + `}`), nil
			}), []string{"libs"})
			out, err := c.SearchPackages(context.Background(), search.PackageFilters{PackageType: test.kind, NamePattern: test.name, Group: test.group})
			if err != nil || len(out.Packages) != 1 {
				t.Fatalf("search: %+v %v", out, err)
			}
			p := out.Packages[0]
			if p.Name != test.name || p.Group != test.group || p.Version != test.version || len(p.Paths) != 1 || p.Repository != "libs" || p.PackageType != test.kind {
				t.Fatalf("incorrect package: %+v", p)
			}
			versions, err := c.ListPackageVersions(context.Background(), PackageVersionsInput{PackageType: test.kind, Name: test.name, Group: test.group})
			if err != nil || len(versions.Versions) != 1 || versions.Versions[0].Version != test.version || versions.Page.Paging != "local_output" || calls != 2 {
				t.Fatalf("version listing: %+v %v", versions, err)
			}
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded), "do-not-return") || strings.Contains(string(encoded), "env.secret") {
				t.Fatal("unrelated properties exposed")
			}
		})
	}
}

func TestPackageGroupingAndLocalPaging(t *testing.T) {
	response := `{"results":[
		{"repo":"libs","path":"b","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.version","value":"2"}]},
		{"repo":"libs","path":"a","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.version","value":"2"}]},
		{"repo":"libs","path":"a","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.version","value":"2"}]},
		{"repo":"libs","path":"c","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.version","value":"10"}]},
		{"repo":"libs","path":"d","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"}]}],"notices":["upstream advice"]}`
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { return []byte(response), nil }), []string{"libs"})
	out, err := c.ListPackageVersions(context.Background(), PackageVersionsInput{PackageType: "npm", Name: "widget", Limit: 1, Offset: 1})
	if err != nil || len(out.Versions) != 1 || out.Versions[0].Version != "2" || len(out.Versions[0].Paths) != 2 || out.Versions[0].Paths[0] != "a/widget.tgz" || out.Page.HasMore || *out.Page.Total != 2 || len(out.Notices) != 2 {
		t.Fatalf("grouping/paging: %+v %v", out, err)
	}
}

func TestPackageFailuresAndScope(t *testing.T) {
	for _, test := range []struct{ name, response, category string }{
		{"missing array", `{}`, "unavailable"},
		{"malformed", `{`, "unavailable"},
		{"null", `null`, "unavailable"},
		{"scope", `{"results":[{"repo":"other","path":".","name":"a"}]}`, "forbidden"},
		{"requested scope", `{"results":[{"repo":"libs2","path":".","name":"a"}]}`, "forbidden"},
		{"traversal", `{"results":[{"repo":"libs","path":"../x","name":"a"}]}`, "unavailable"},
		{"truncated", `{"results":[],"range":{"start_pos":0,"end_pos":0,"total":0,"notification":"trimmed"}}`, "unavailable"},
		{"partial range", `{"results":[],"range":{"start_pos":0,"end_pos":0,"total":10}}`, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { return []byte(test.response), nil }), []string{"libs", "libs2"})
			_, err := c.SearchPackages(context.Background(), search.PackageFilters{PackageType: "npm", Repositories: []string{"libs"}})
			if err == nil || Classify(err).Category != test.category {
				t.Fatalf("expected %s: %v", test.category, err)
			}
		})
	}
	calls := 0
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { calls++; return nil, nil }), []string{"libs"})
	_, err := c.SearchPackages(context.Background(), search.PackageFilters{PackageType: "npm", Repositories: []string{"other"}})
	if err == nil || Classify(err).Category != "forbidden" {
		t.Fatal(err)
	}
	_, err = c.ListPackageVersions(context.Background(), PackageVersionsInput{PackageType: "npm", Name: "../a"})
	if err == nil || Classify(err).Category != "invalid_input" || calls != 0 {
		t.Fatalf("validation reached executor: %v calls=%d", err, calls)
	}
}

func TestPackageLiteralWildcardSemantics(t *testing.T) {
	if !packageNameMatcher("包?", false).MatchString("包名") || packageNameMatcher("包?", false).MatchString("包名字") {
		t.Fatal("question mark must match exactly one Unicode character")
	}
	if !packageNameMatcher("@acme/*", false).MatchString("@acme/widget") || !packageNameMatcher("pkg[1]", false).MatchString("pkg[1]") || packageNameMatcher("pkg[1]", false).MatchString("pkg1") {
		t.Fatal("package patterns have incorrect wildcard semantics")
	}
	if !packageNameMatcher("pkg.1", true).MatchString("pkg.1") || packageNameMatcher("pkg.1", true).MatchString("pkgX1") {
		t.Fatal("exact names interpreted as expressions")
	}
}

func TestDockerMultiArchitectureTags(t *testing.T) {
	digest := strings.Repeat("a", 64)
	response := `{"results":[
		{"repo":"libs","path":"acme/widget/stable","name":"list.manifest.json","properties":[{"key":"docker.repoName","value":"acme/widget"}]},
		{"repo":"libs","path":"acme/widget/sha256__` + digest + `","name":"manifest.json","properties":[{"key":"docker.repoName","value":"acme/widget"}]},
		{"repo":"libs","path":"acme/widget/sha256:` + digest + `","name":"manifest.json","properties":[{"key":"docker.repoName","value":"acme/widget"}]}]}`
	c := New(executeFunc(func(_ context.Context, _, _ string, body []byte) ([]byte, error) {
		if !strings.Contains(string(body), `"name":"list.manifest.json"`) {
			t.Fatal("multi-architecture manifest not queried")
		}
		return []byte(response), nil
	}), []string{"libs"})
	out, err := c.ListPackageVersions(context.Background(), PackageVersionsInput{PackageType: "docker", Name: "acme/widget"})
	if err != nil || len(out.Versions) != 1 || out.Versions[0].Version != "stable" || out.Versions[0].Paths[0] != "acme/widget/stable/list.manifest.json" || len(out.Notices) != 0 {
		t.Fatalf("multi-architecture versions: %+v %v", out, err)
	}
}

func TestAmbiguousPackageMetadata(t *testing.T) {
	response := `{"results":[{"repo":"libs","path":".","name":"widget.tgz","properties":[{"key":"npm.name","value":"widget"},{"key":"npm.name","value":"other"},{"key":"npm.version","value":"1"}]}]}`
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { return []byte(response), nil }), []string{"libs"})
	out, err := c.SearchPackages(context.Background(), search.PackageFilters{PackageType: "npm"})
	if err != nil || len(out.Packages) != 0 || len(out.Notices) != 1 {
		t.Fatalf("ambiguous identity exposed: %+v %v", out, err)
	}
}
