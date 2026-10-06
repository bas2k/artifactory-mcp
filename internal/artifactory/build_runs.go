package artifactory

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/search"
)

type BuildRunsInput struct {
	Name    string `json:"name" jsonschema:"Exact build name"`
	Project string `json:"project,omitempty"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Output page size from 1 to 500; defaults to 100; does not reduce upstream fetch"`
	Offset  int    `json:"offset,omitempty"`
}

type BuildRun struct {
	Number  string `json:"number"`
	Started string `json:"started,omitempty" jsonschema:"Pass this timestamp to get_build_info to select this occurrence"`
}

type BuildRuns struct {
	Runs []BuildRun `json:"runs"`
	Page Page       `json:"page"`
}

func buildStarted(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	started, err := time.Parse("2006-01-02T15:04:05.000-0700", value)
	if err != nil {
		return time.Parse(time.RFC3339Nano, value)
	}
	return started, nil
}

func (c *Client) ListBuildRuns(ctx context.Context, in BuildRunsInput) (BuildRuns, error) {
	out := BuildRuns{Runs: []BuildRun{}}
	if config.ValidateSegment(in.Name) != nil {
		return out, NewError("invalid_input", "invalid build name")
	}
	limit, err := search.Paging(in.Limit, in.Offset)
	if err != nil {
		return out, invalid(err)
	}
	q, err := projectQuery(in.Project)
	if err != nil {
		return out, err
	}
	var raw struct {
		Runs []struct {
			URI     string `json:"uri"`
			Started string `json:"started"`
		} `json:"buildsNumbers"`
	}
	if err := c.read(ctx, "build_runs", queryPath("api/build/"+url.PathEscape(in.Name), q), nil, &raw); err != nil {
		return out, err
	}
	if raw.Runs == nil {
		return out, NewError("unavailable", "upstream omitted the build runs array")
	}
	type datedRun struct {
		run     BuildRun
		started time.Time
	}
	runs := make([]datedRun, 0, len(raw.Runs))
	for _, run := range raw.Runs {
		uri := strings.TrimPrefix(run.URI, "/")
		if strings.Contains(uri, "/") {
			return out, NewError("unavailable", "upstream returned an invalid build number")
		}
		number, err := url.PathUnescape(uri)
		if err != nil || config.ValidateSegment(number) != nil {
			return out, NewError("unavailable", "upstream returned an invalid build number")
		}
		started, err := buildStarted(run.Started)
		if err != nil {
			return out, NewError("unavailable", "upstream returned an invalid build start time")
		}
		runs = append(runs, datedRun{BuildRun{Number: number, Started: run.Started}, started})
	}
	slices.SortStableFunc(runs, func(a, b datedRun) int {
		return cmp.Or(b.started.Compare(a.started), cmp.Compare(a.run.Number, b.run.Number))
	})
	for _, run := range runs {
		out.Runs = append(out.Runs, run.run)
	}
	out.Runs, out.Page = localPage(out.Runs, limit, in.Offset)
	return out, nil
}
