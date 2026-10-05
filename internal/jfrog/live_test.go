//go:build integration

package jfrog

import (
	"context"
	"os"
	"strings"
	"testing"

	app "artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/search"
)

// This suite requires operator-provided, existing read-only fixtures. It never
// creates, uploads, or deletes data and does not guess the installation license.
func TestLiveRestrictedToken(t *testing.T) {
	if os.Getenv("ARTIFACTORY_INTEGRATION") != "1" {
		t.Skip("set ARTIFACTORY_INTEGRATION=1 to opt in")
	}
	required := []string{"ARTIFACTORY_TEST_REPOSITORY", "ARTIFACTORY_TEST_ARTIFACT", "ARTIFACTORY_TEST_MISSING_ARTIFACT", "ARTIFACTORY_TEST_BUILD_NAME", "ARTIFACTORY_TEST_BUILD_NUMBER", "ARTIFACTORY_TEST_PROJECT", "ARTIFACTORY_TEST_VERSION", "ARTIFACTORY_TEST_LICENSE"}
	for _, key := range required {
		if os.Getenv(key) == "" {
			t.Fatalf("%s is required for live validation", key)
		}
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Logf("operator-declared Artifactory version=%s license=%s", os.Getenv("ARTIFACTORY_TEST_VERSION"), os.Getenv("ARTIFACTORY_TEST_LICENSE"))
	ctx := context.Background()
	repo := os.Getenv("ARTIFACTORY_TEST_REPOSITORY")
	artifact := os.Getenv("ARTIFACTORY_TEST_ARTIFACT")
	t.Run("discovery", func(t *testing.T) {
		_, err := c.ListRepositories(ctx, app.RepositoryFilter{})
		if os.Getenv("ARTIFACTORY_TEST_DISCOVERY_FORBIDDEN") == "1" {
			if err == nil || app.Classify(err).Category != "forbidden" {
				t.Fatalf("expected discovery permission failure: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("known_repository", func(t *testing.T) {
		if _, err := c.ArtifactInfo(ctx, app.ArtifactInput{Repository: repo, Path: artifact}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("search_pages", func(t *testing.T) {
		first, err := c.Search(ctx, search.Filters{Repositories: []string{repo}, Limit: 1})
		if err != nil || len(first.Artifacts) != 1 {
			t.Fatalf("first page: %v", err)
		}
		second, err := c.Search(ctx, search.Filters{Repositories: []string{repo}, Limit: 1, Offset: 1})
		if err != nil || len(second.Artifacts) != 1 {
			t.Fatalf("fixture repository needs at least two readable artifacts: %v", err)
		}
		if first.Artifacts[0].Path == second.Artifacts[0].Path && first.Artifacts[0].Name == second.Artifacts[0].Name {
			t.Fatal("pages repeated the same artifact")
		}
	})
	t.Run("metadata", func(t *testing.T) {
		if _, err := c.Properties(ctx, app.PropertiesInput{Repository: repo, Path: artifact}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Stats(ctx, app.ArtifactInput{Repository: repo, Path: artifact}); err != nil {
			t.Fatal(err)
		}
		folder := ""
		if i := strings.LastIndex(artifact, "/"); i >= 0 {
			folder = artifact[:i]
		}
		if _, err := c.Folder(ctx, app.ArtifactInput{repo, folder}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing_artifact", func(t *testing.T) {
		_, err := c.ArtifactInfo(ctx, app.ArtifactInput{Repository: repo, Path: os.Getenv("ARTIFACTORY_TEST_MISSING_ARTIFACT")})
		if err == nil || app.Classify(err).Category != "not_found" {
			t.Fatalf("missing artifact: %v", err)
		}
	})
	t.Run("project_build", func(t *testing.T) {
		project := os.Getenv("ARTIFACTORY_TEST_PROJECT")
		name := os.Getenv("ARTIFACTORY_TEST_BUILD_NAME")
		if _, err := c.ListBuilds(ctx, app.BuildsInput{Project: project, NameFilter: name}); err != nil {
			t.Fatal(err)
		}
		out, err := c.BuildInfo(ctx, app.BuildInput{Name: name, Number: os.Getenv("ARTIFACTORY_TEST_BUILD_NUMBER"), Project: project, Started: os.Getenv("ARTIFACTORY_TEST_BUILD_STARTED"), DetailLimit: 1})
		if err != nil || len(out.Modules) == 0 {
			t.Fatalf("fixture build needs at least one readable module: %v", err)
		}
	})
}
