package federation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/federation"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/storage"
)

func TestFederatedExactIdentityTraversalAndProvenance(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	firstRoot := makeRepo(t, "first")
	secondRoot := makeRepo(t, "second")
	entries := register(t, catalogPath, firstRoot, secondRoot)

	target := "scip go gomod example.com/shared 1.0.0 Shared#"
	caller := "scip go gomod example.com/app 1.0.0 Call()."
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
		Unit: graph.Unit{ID: "app", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{
			fixtureSymbol("app", caller, "Call", "call"),
			fixtureSymbol("app", "scip go gomod example.com/app 1.0.0 Duplicate.", "Duplicate", "duplicate-a"),
		},
		Edges: []graph.Edge{{ID: "call-edge", UnitID: "app", From: caller, To: target, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"}},
	})
	writeFacts(t, entries[1].DatabasePath, graph.UnitFacts{
		Unit: graph.Unit{ID: "shared", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{
			fixtureSymbol("shared", target, "Shared", "shared"),
			fixtureSymbol("shared", "scip go gomod example.com/shared 1.0.0 Duplicate.", "Duplicate", "duplicate-b"),
		},
	})

	store, err := federation.Open(ctx, catalogPath, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resolved, err := query.Resolve(ctx, store, target)
	if err != nil || resolved.ID != target {
		t.Fatalf("Resolve = %#v, %v", resolved, err)
	}
	callers, truncated, err := store.EdgesTo(ctx, target, []graph.EdgeKind{graph.EdgeCalls}, 10)
	if err != nil || truncated || len(callers) != 1 || callers[0].From != caller {
		t.Fatalf("callers = %#v, %t, %v", callers, truncated, err)
	}
	path, err := query.Path(ctx, store, caller, target, []graph.EdgeKind{graph.EdgeCalls}, query.Bounds{MaxDepth: 4, MaxNodes: 20, MaxEdges: 100})
	if err != nil || !reflect.DeepEqual(path.Nodes, []string{caller, target}) {
		t.Fatalf("Path = %#v, %v", path, err)
	}
	impact, err := query.Impact(ctx, store, target, []graph.EdgeKind{graph.EdgeCalls}, query.Bounds{MaxDepth: 4, MaxNodes: 20, MaxEdges: 100})
	if err != nil || !reflect.DeepEqual(impact.Nodes, []string{target, caller}) {
		t.Fatalf("Impact = %#v, %v", impact, err)
	}
	duplicates, _, err := store.FindSymbols(ctx, "Duplicate", 10)
	if err != nil || len(duplicates) != 2 || duplicates[0].ID == duplicates[1].ID {
		t.Fatalf("duplicate names = %#v, %v", duplicates, err)
	}
	sources := store.Sources()
	if len(sources) < 3 {
		t.Fatalf("sources = %#v", sources)
	}
	for i := 1; i < len(sources); i++ {
		if sources[i-1].Kind > sources[i].Kind {
			t.Fatalf("sources not deterministic: %#v", sources)
		}
	}
}

func TestFederationReportsMissingAndUnavailableMembers(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	healthyRoot := makeRepo(t, "healthy")
	missingRoot := makeRepo(t, "missing")
	entries := register(t, catalogPath, healthyRoot, missingRoot)
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{Unit: graph.Unit{ID: "healthy", Provider: "fixture", ProviderVersion: "1"}, Symbols: []graph.Symbol{fixtureSymbol("healthy", "global", "Global", "global")}})
	if err := os.Rename(missingRoot, missingRoot+"-gone"); err != nil {
		t.Fatal(err)
	}
	store, err := federation.Open(ctx, catalogPath, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	values, _, err := store.FindSymbols(ctx, "Global", 10)
	if err != nil || len(values) != 1 {
		t.Fatalf("healthy partial result = %#v, %v", values, err)
	}
	diagnostics := strings.Join(store.Diagnostics(), "\n")
	if !strings.Contains(diagnostics, "missing worktree") {
		t.Fatalf("diagnostics = %q", diagnostics)
	}
	if _, err := federation.Open(ctx, catalogPath, nil, 1); err == nil || !strings.Contains(err.Error(), "exceeds --max-repos") {
		t.Fatalf("fan-out error = %v", err)
	}
}

func fixtureSymbol(unit, id, name, suffix string) graph.Symbol {
	return graph.Symbol{ID: id, UnitID: unit, StableName: id, DisplayName: name, Kind: "function", Provider: "fixture", Evidence: graph.EvidenceExact, DocumentID: "", Definition: graph.Range{}, NormalizedName: strings.ToLower(name + suffix)}
}

func register(t *testing.T, path string, roots ...string) []catalog.Entry {
	t.Helper()
	db, err := catalog.Open(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries := make([]catalog.Entry, len(roots))
	for i, root := range roots {
		entries[i], err = db.Add(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
	}
	return entries
}

func writeFacts(t *testing.T, path string, facts graph.UnitFacts) {
	t.Helper()
	db, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ReplaceUnit(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
}

func makeRepo(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}}
	for _, args := range commands {
		runGit(t, root, args...)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "initial")
	runGit(t, root, "remote", "add", "origin", "https://github.com/acme/"+name+".git")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
