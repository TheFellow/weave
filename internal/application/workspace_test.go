package application

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
)

func TestWorkspaceQueriesAreStrictAndAggregateSectionLinks(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, databasePath, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	unit := workspaceFixture()
	if err := db.ReplaceUnit(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	app := Local{DatabasePath: databasePath}
	found, err := app.Execute(ctx, Invocation{Command: "workspace find", Arguments: []string{"publication lifetime independently"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Symbols) != 1 || found.Symbols[0].ID != "design" {
		t.Fatalf("phrase find = %#v", found)
	}

	outline, err := app.Execute(ctx, Invocation{Command: "workspace outline", Arguments: []string{"README.md"}, Limit: 20, MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(outline.Symbols) != 2 || len(outline.Edges) != 1 || outline.Symbols[1].StableName != "README.md#design" || outline.Truncated {
		t.Fatalf("outline = %#v", outline)
	}
	links, err := app.Execute(ctx, Invocation{Command: "workspace links", Arguments: []string{"README.md"}, Limit: 20, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(links.Edges) != 2 || links.Edges[0].Kind != graph.EdgeLinksTo || len(links.Symbols) != 2 {
		t.Fatalf("links = %#v", links)
	}
	backlinks, err := app.Execute(ctx, Invocation{Command: "workspace backlinks", Arguments: []string{"docs/guide.md"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(backlinks.Edges) != 2 || len(backlinks.Symbols) != 1 || backlinks.Symbols[0].StableName != "README.md#design" {
		t.Fatalf("backlinks = %#v", backlinks)
	}
	impact, err := app.Execute(ctx, Invocation{Command: "impact", ImpactFiles: []string{"docs/guide.md"}, Limit: 20, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(impact.Nodes, "design") {
		t.Fatalf("content backlink absent from file impact: %#v", impact)
	}
}

func workspaceFixture() graph.UnitFacts {
	zero := graph.Range{Start: graph.Position{Byte: 0}, End: graph.Position{Byte: 0}}
	return graph.UnitFacts{
		Unit: graph.Unit{ID: "workspace-fixture", Provider: "test", ProviderVersion: "1"},
		Documents: []graph.Document{
			{ID: "readme-doc", UnitID: "workspace-fixture", Path: "README.md", Language: "markdown", Provider: "test", ProviderVersion: "1"},
			{ID: "guide-doc", UnitID: "workspace-fixture", Path: "docs/guide.md", Language: "markdown", Provider: "test", ProviderVersion: "1"},
		},
		Symbols: []graph.Symbol{
			{ID: "readme", UnitID: "workspace-fixture", StableName: "README.md", DisplayName: "Readme", Kind: "document", DocumentID: "readme-doc", Definition: zero, Provider: "test", Evidence: graph.EvidenceSyntactic},
			{ID: "design", UnitID: "workspace-fixture", StableName: "README.md#design", DisplayName: "Design", SearchTerms: []string{"independently", "lifetime", "publication"}, Kind: "section", DocumentID: "readme-doc", Definition: zero, Provider: "test", Evidence: graph.EvidenceSyntactic},
			{ID: "guide", UnitID: "workspace-fixture", StableName: "docs/guide.md", DisplayName: "Guide", Kind: "document", DocumentID: "guide-doc", Definition: zero, Provider: "test", Evidence: graph.EvidenceSyntactic},
			{ID: "guide-section", UnitID: "workspace-fixture", StableName: "docs/guide.md#details", DisplayName: "Details", Kind: "section", DocumentID: "guide-doc", Definition: zero, Provider: "test", Evidence: graph.EvidenceSyntactic},
		},
		Edges: []graph.Edge{
			{ID: "contains", UnitID: "workspace-fixture", From: "readme", To: "design", Kind: graph.EdgeContains, Evidence: graph.EvidenceSyntactic, Provider: "test"},
			{ID: "links", UnitID: "workspace-fixture", From: "design", To: "guide", Kind: graph.EdgeLinksTo, Evidence: graph.EvidenceDeclared, Provider: "test"},
			{ID: "guide-contains", UnitID: "workspace-fixture", From: "guide", To: "guide-section", Kind: graph.EdgeContains, Evidence: graph.EvidenceSyntactic, Provider: "test"},
			{ID: "links-section", UnitID: "workspace-fixture", From: "design", To: "guide-section", Kind: graph.EdgeLinksTo, Evidence: graph.EvidenceDeclared, Provider: "test"},
		},
	}
}
