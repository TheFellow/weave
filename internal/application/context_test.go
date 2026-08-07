package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
)

func TestFederatedContextRefreshesAndReadsOwningWorktree(t *testing.T) {
	ctx := context.Background()
	root := contextRepository(t, "func Shared() {}\n")
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	catalogDB, err := catalog.Open(ctx, catalogPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := catalogDB.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogDB.Close(); err != nil {
		t.Fatal(err)
	}
	provider := contextProvider{}
	app := Local{
		CatalogPath: catalogPath,
		FreshnessFor: func(directory string) *freshness.Manager {
			return &freshness.Manager{Directory: directory, Provider: provider, Command: "context test"}
		},
	}
	response, err := app.Execute(ctx, Invocation{
		Command: "context", Arguments: []string{"shared-id"}, Scope: "catalog",
		Limit: 8, ContextLines: 0, MaxSourceBytes: 4096, MaxRepos: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Context == nil || response.Context.Schema != contextquery.Schema || response.Context.Metadata.Scope != "catalog" {
		t.Fatalf("context = %#v", response.Context)
	}
	metadata := response.Context.Metadata
	if !metadata.Freshness.Checked || !metadata.Freshness.Current || metadata.Freshness.Partial || response.Truncated {
		t.Fatalf("freshness = %#v, truncated=%t", metadata.Freshness, response.Truncated)
	}
	if len(response.Context.Focus.Repositories) != 1 || response.Context.Focus.Repositories[0].Identity != entry.Identity {
		t.Fatalf("focus provenance = %#v", response.Context.Focus.Repositories)
	}
	if len(response.Context.Evidence) != 1 || response.Context.Evidence[0].Source.Status != contextquery.SourceCurrent || response.Context.Evidence[0].Source.Lines[0].Text != "func Shared() {}" {
		t.Fatalf("evidence = %#v", response.Context.Evidence)
	}
	if len(response.Sources) < 3 {
		t.Fatalf("fact provenance = %#v", response.Sources)
	}
}

type contextProvider struct{}

func (contextProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "context-fixture", Version: "1"}
}

func (contextProvider) Refresh(_ context.Context, request freshness.Request) (freshness.Result, error) {
	content, err := os.ReadFile(filepath.Join(request.Repository.Root, "shared.go"))
	if err != nil {
		return freshness.Result{}, err
	}
	digest := sha256.Sum256(content)
	hash := "sha256:" + hex.EncodeToString(digest[:])
	rng := graph.Range{Start: graph.Position{Line: 0, Column: 5, Byte: 5}, End: graph.Position{Line: 0, Column: 11, Byte: 11}}
	facts := graph.UnitFacts{
		Unit: graph.Unit{ID: "unit", Provider: "context-fixture", ProviderVersion: "1", InputFingerprint: hash},
		Documents: []graph.Document{{
			ID: "document-id", UnitID: "unit", Path: "shared.go", Language: "go", ContentHash: hash,
			Provider: "context-fixture", ProviderVersion: "1",
		}},
		Symbols: []graph.Symbol{{
			ID: "shared-id", UnitID: "unit", StableName: "fixture.Shared", DisplayName: "Shared", Kind: "function",
			DocumentID: "document-id", Definition: rng, Provider: "context-fixture", Evidence: graph.EvidenceExact,
		}},
		Occurrences: []graph.Occurrence{{
			ID: "definition-id", UnitID: "unit", SymbolID: "shared-id", DocumentID: "document-id", Role: "definition",
			Range: rng, Provider: "context-fixture", Evidence: graph.EvidenceExact,
		}},
	}
	return freshness.Result{
		Batches: []graph.UnitFacts{facts},
		Units:   []freshness.Unit{{ID: facts.Unit.ID, InputFingerprint: hash}},
	}, nil
}

func contextRepository(t *testing.T, content string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "weave@example.test"}, {"config", "user.name", "Weave Test"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "shared.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "fixture"}, {"remote", "add", "origin", "https://github.com/example/context-application.git"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}
