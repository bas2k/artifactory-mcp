package artifactory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"artifactory-mcp/internal/search"
)

type executeFunc func(context.Context, string, string, []byte) ([]byte, error)

func (f executeFunc) Execute(ctx context.Context, op, path string, b []byte) ([]byte, error) {
	return f(ctx, op, path, b)
}
func TestScopeCannotReachExecutor(t *testing.T) {
	calls := 0
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil }), []string{"libs"})
	_, err := c.ArtifactInfo(context.Background(), ArtifactInput{"other", "a.jar"})
	if Classify(err).Category != "forbidden" {
		t.Fatal(err)
	}
	_, err = c.Search(context.Background(), search.Filters{Repositories: []string{"other"}})
	if Classify(err).Category != "forbidden" {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("disallowed repository reached upstream")
	}
}
func TestBuildSanitizationAndPaging(t *testing.T) {
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) {
		return []byte(`{"buildInfo":{"name":"job","number":"1","started":"2026-01-01T00:00:00Z","properties":{"env.password":"secret"},"modules":[{"id":"module","properties":{"secret":"value"},"artifacts":[{"name":"a","properties":{"secret":"value"}},{"name":"b"}],"dependencies":[{"id":"x"},{"id":"y"}]}]}}`), nil
	}), nil)
	out, err := c.BuildInfo(context.Background(), BuildInput{Name: "job", Number: "1", ModuleLimit: 1, DetailLimit: 1, DetailOffset: 1})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "properties") {
		t.Fatal("build environment data leaked")
	}
	if out.Modules[0].Artifacts[0].Name != "b" || out.Modules[0].ArtifactsPage.HasMore || *out.Modules[0].ArtifactsPage.Total != 2 {
		t.Fatalf("unexpected detail paging: %+v", out)
	}
}
func TestSearchRangeIsNotTotal(t *testing.T) {
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) {
		return []byte(`{"results":[{"repo":"libs","path":".","name":"a","size":9007199254740993}],"range":{"start_pos":0,"end_pos":1,"total":1},"notices":["query truncated"]}`), nil
	}), []string{"libs"})
	out, err := c.Search(context.Background(), search.Filters{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Page.Total != nil || !out.Page.HasMore || out.Artifacts[0].Size != 9007199254740993 || len(out.Notices) != 1 {
		t.Fatalf("lost paging, exact size, or notice: %+v", out)
	}
}
func TestRedactionAndMalformedJSON(t *testing.T) {
	out, err := RedactJSON([]byte(`{"value":"my secret token","n":9007199254740993}`), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret") || !strings.Contains(string(out), "9007199254740993") {
		t.Fatal("redaction lost precision or leaked credential")
	}
	for _, bad := range []string{`{"a":}`, `{} {}`, `null`} {
		var value struct{}
		data, err := RedactJSON([]byte(bad), "secret")
		if err == nil {
			err = Decode(data, &value)
		}
		if err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestMissingResponseEnvelopesAreErrors(t *testing.T) {
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { return []byte(`{}`), nil }), nil)
	ctx := context.Background()
	calls := []func() error{
		func() error { _, err := c.Search(ctx, search.Filters{}); return err },
		func() error {
			_, err := c.ArtifactInfo(ctx, ArtifactInput{Repository: "libs", Path: "a.jar"})
			return err
		},
		func() error { _, err := c.Folder(ctx, ArtifactInput{Repository: "libs"}); return err },
		func() error {
			_, err := c.Properties(ctx, PropertiesInput{Repository: "libs", Path: "a.jar"})
			return err
		},
		func() error { _, err := c.ListBuilds(ctx, BuildsInput{}); return err },
		func() error { _, err := c.BuildInfo(ctx, BuildInput{Name: "job", Number: "1"}); return err },
	}
	for _, call := range calls {
		err := call()
		if err == nil || Classify(err).Category != "unavailable" {
			t.Fatalf("missing response fields were treated as success: %v", err)
		}
	}
}
