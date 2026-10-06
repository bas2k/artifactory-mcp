package artifactory

import (
	"context"
	"slices"
	"testing"
)

func TestBuildRunsPagingAndOccurrences(t *testing.T) {
	c := New(executeFunc(func(_ context.Context, op, path string, _ []byte) ([]byte, error) {
		if op != "build_runs" || path != "api/build/sample%20job?project=p" {
			t.Fatalf("unexpected endpoint: %s %s", op, path)
		}
		return []byte(`{"buildsNumbers":[{"uri":"/42","started":"2026-01-01T12:00:00.000+0000"},{"uri":"/alpha%20run","started":"2026-01-02T12:00:00Z"},{"uri":"/42","started":"2026-01-01T16:00:00.000+0500"}]}`), nil
	}), []string{"unrelated-repository"})
	out, err := c.ListBuildRuns(context.Background(), BuildRunsInput{Name: "sample job", Project: "p", Limit: 1, Offset: 1})
	if err != nil || len(out.Runs) != 1 || out.Runs[0].Number != "42" || out.Runs[0].Started != "2026-01-01T12:00:00.000+0000" || !out.Page.HasMore || *out.Page.Total != 3 || out.Page.Paging != "local_output" {
		t.Fatalf("build runs: %+v %v", out, err)
	}
}

func TestBuildRunsOrdering(t *testing.T) {
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) {
		return []byte(`{"buildsNumbers":[
			{"uri":"/2","started":"2026-01-01T12:00:00Z"},
			{"uri":"/10","started":"2026-01-01T12:00:00.000+0000"},
			{"uri":"/latest","started":"2026-01-02T12:00:00Z"},
			{"uri":"/10","started":"2026-01-01T17:00:00.000+0500"},
			{"uri":"/undated"},
			{"uri":"/old","started":"2025-01-01T12:00:00Z"}]}`), nil
	}), nil)
	out, err := c.ListBuildRuns(context.Background(), BuildRunsInput{Name: "job"})
	want := []BuildRun{
		{Number: "latest", Started: "2026-01-02T12:00:00Z"},
		{Number: "10", Started: "2026-01-01T12:00:00.000+0000"},
		{Number: "10", Started: "2026-01-01T17:00:00.000+0500"},
		{Number: "2", Started: "2026-01-01T12:00:00Z"},
		{Number: "old", Started: "2025-01-01T12:00:00Z"},
		{Number: "undated"},
	}
	if err != nil || !slices.Equal(out.Runs, want) {
		t.Fatalf("build run order: %+v, error: %v", out.Runs, err)
	}
}

func TestBuildRunFailures(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"buildsNumbers":[{"uri":"/../x"}]}`, `{"buildsNumbers":[{"uri":"/%2Fsecret"}]}`, `{"buildsNumbers":[{"uri":"/https:%2F%2Fexample.test"}]}`, `{"buildsNumbers":[{"uri":"/42","started":"yesterday"}]}`} {
		c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) { return []byte(body), nil }), nil)
		if _, err := c.ListBuildRuns(context.Background(), BuildRunsInput{Name: "job"}); err == nil || Classify(err).Category != "unavailable" {
			t.Fatalf("accepted %s: %v", body, err)
		}
	}
	calls := 0
	c := New(executeFunc(func(context.Context, string, string, []byte) ([]byte, error) {
		calls++
		return []byte(`{"buildsNumbers":[]}`), nil
	}), nil)
	for _, in := range []BuildRunsInput{{Name: "../job"}, {Name: "job", Project: "../p"}, {Name: "job", Limit: 501}, {Name: "job", Offset: -1}} {
		if _, err := c.ListBuildRuns(context.Background(), in); err == nil || Classify(err).Category != "invalid_input" {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached upstream")
	}
	out, err := c.ListBuildRuns(context.Background(), BuildRunsInput{Name: "job", Offset: 100})
	if err != nil || len(out.Runs) != 0 || out.Page.HasMore || *out.Page.Total != 0 {
		t.Fatalf("empty listing: %+v %v", out, err)
	}
}
