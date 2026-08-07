package application

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/workspaceindex"
)

func TestAuthoredLinksResolveHeterogeneousResourcesAndRefreshOrdinaryEdges(t *testing.T) {
	ctx := context.Background()
	root := contextualLinkRepository(t, "local")
	manager := contextualLinkManager(root)
	app := Local{Freshness: manager}

	added, err := app.Execute(ctx, Invocation{
		Command: "links add", Arguments: []string{"guide-documents-details"},
		LinkFrom: "README.md#overview", LinkTo: "docs/guide.md#details", LinkKind: graph.EdgeDocuments,
		LinkNote:    "The overview explains the detailed guide.",
		LinkFromSet: true, LinkToSet: true, LinkKindSet: true, LinkNoteSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added.Links) != 1 || added.Links[0].Kind != graph.EdgeDocuments || added.Freshness == nil || !added.Freshness.Refreshed {
		t.Fatalf("add response = %#v", added)
	}
	from, err := bridge.Endpoint(added.Links[0].From)
	if err != nil {
		t.Fatal(err)
	}
	to, err := bridge.Endpoint(added.Links[0].To)
	if err != nil {
		t.Fatal(err)
	}
	path, err := app.Execute(ctx, Invocation{
		Command: "path", Arguments: []string{from, to}, Kinds: []graph.EdgeKind{graph.EdgeDocuments},
		Limit: 10, MaxDepth: 2,
	})
	if err != nil || len(path.Edges) != 1 || path.Edges[0].Provider != bridge.ProviderName || path.Edges[0].Evidence != graph.EvidenceDeclared {
		t.Fatalf("authored graph path = %#v, %v", path, err)
	}
	contentLinks, err := app.Execute(ctx, Invocation{Command: "workspace links", Arguments: []string{from}, Limit: 20, MaxDepth: 2})
	if err != nil || !slices.ContainsFunc(contentLinks.Edges, func(edge graph.Edge) bool {
		return edge.Kind == graph.EdgeDocuments && edge.To == to
	}) {
		t.Fatalf("workspace links omitted authored document relationship = %#v, %v", contentLinks, err)
	}

	updated, err := app.Execute(ctx, Invocation{
		Command: "links update", Arguments: []string{"guide-documents-details"},
		LinkTo: "id:git-commit:0123456789abcdef", LinkNote: "Pinned to an intentionally open immutable resource.",
		LinkToSet: true, LinkNoteSet: true,
	})
	if err != nil || len(updated.Links) != 1 || updated.Links[0].To != "entity:git-commit:0123456789abcdef" {
		t.Fatalf("update response = %#v, %v", updated, err)
	}
	listed, err := app.Execute(ctx, Invocation{Command: "links list"})
	if err != nil || len(listed.Links) != 1 || listed.Links[0] != updated.Links[0] {
		t.Fatalf("list response = %#v, %v", listed, err)
	}
	removed, err := app.Execute(ctx, Invocation{Command: "links remove", Arguments: []string{"guide-documents-details"}})
	if err != nil || len(removed.Links) != 1 {
		t.Fatalf("remove response = %#v, %v", removed, err)
	}
	empty, err := app.Execute(ctx, Invocation{Command: "links list"})
	if err != nil || len(empty.Links) != 0 {
		t.Fatalf("empty list = %#v, %v", empty, err)
	}
}

func TestAuthoredLinksResolveAcrossCatalogRepositories(t *testing.T) {
	ctx := context.Background()
	firstRoot := contextualLinkRepository(t, "first")
	secondRoot := contextualLinkRepository(t, "second")
	firstManager, secondManager := contextualLinkManager(firstRoot), contextualLinkManager(secondRoot)
	if _, err := firstManager.Ensure(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := secondManager.Ensure(ctx, false); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	db, err := catalog.Open(ctx, catalogPath, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Add(ctx, firstRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Add(ctx, secondRoot); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	firstRepo, err := repository.Discover(ctx, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondRepo, err := repository.Discover(ctx, secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	managers := map[string]*freshness.Manager{firstRepo.Root: firstManager, secondRepo.Root: secondManager}
	app := Local{
		Freshness: firstManager, CatalogPath: catalogPath,
		FreshnessFor: func(root string) *freshness.Manager { return managers[filepath.Clean(root)] },
	}
	response, err := app.Execute(ctx, Invocation{
		Command: "links add", Arguments: []string{"cross-repository-guides"},
		LinkFrom: "README-first.md#overview", LinkTo: "README-second.md#overview", LinkKind: graph.EdgeLinksTo,
		LinkFromSet: true, LinkToSet: true, LinkKindSet: true,
		Scope: "catalog", MaxRepos: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Links) != 1 || len(response.Sources) != 2 {
		t.Fatalf("catalog link response = %#v", response)
	}
	repositories := []string{response.Sources[0].Repository, response.Sources[1].Repository}
	slices.Sort(repositories)
	if repositories[0] == repositories[1] {
		t.Fatalf("endpoint provenance did not span repositories: %#v", response.Sources)
	}
}

func contextualLinkManager(root string) *freshness.Manager {
	provider := freshness.CompositeProvider{Providers: []freshness.Provider{workspaceindex.Provider{}, bridge.Provider{}}}
	return &freshness.Manager{Directory: root, Provider: provider, Command: "test"}
}

func contextualLinkRepository(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	readme := "README-" + name + ".md"
	if name == "local" {
		readme = "README.md"
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, readme), []byte("# "+name+"\n\n## Overview\n\nContext.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte("# Guide\n\n## Details\n\nDetails.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init", "-q"},
		{"config", "user.email", "weave@example.test"},
		{"config", "user.name", "Weave Test"},
		{"remote", "add", "origin", "https://example.test/" + name + ".git"},
		{"add", "."},
		{"commit", "-qm", "initial"},
	}
	for _, arguments := range commands {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	return root
}
