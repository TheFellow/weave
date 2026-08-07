package federation_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/federation"
	"github.com/TheFellow/weave/internal/freshness"
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
		Edges: []graph.Edge{
			{ID: "call-edge", UnitID: "app", From: caller, To: target, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"},
		},
	})
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
		Unit: graph.Unit{ID: "bridge", Provider: "weave-bridges", ProviderVersion: "1"},
		Edges: []graph.Edge{
			{ID: "declared-bridge", UnitID: "bridge", From: caller, To: target, Kind: graph.EdgeDependsOn, Evidence: graph.EvidenceDeclared, Provider: "weave-bridges"},
		},
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
	if store.Partial() {
		t.Fatalf("healthy federation unexpectedly partial: %q", store.Diagnostics())
	}
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
	bridgePath, err := query.Path(ctx, store, caller, target, []graph.EdgeKind{graph.EdgeDependsOn}, query.Bounds{MaxDepth: 4, MaxNodes: 20, MaxEdges: 100})
	if err != nil || len(bridgePath.Edges) != 1 || bridgePath.Edges[0].Evidence != graph.EvidenceDeclared || bridgePath.Edges[0].Provider != "weave-bridges" {
		t.Fatalf("declared bridge path = %#v, %v", bridgePath, err)
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
	if !store.Partial() {
		t.Fatal("missing selected member did not mark federation partial")
	}
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
	_, err = federation.Open(ctx, catalogPath, []string{entries[0].Identity, "z-missing", "a-missing"}, 8)
	if err == nil || !strings.Contains(err.Error(), "a-missing, z-missing") {
		t.Fatalf("unmatched selector error = %v", err)
	}
}

func TestFederationPartialTracksQueryFailuresButNotRefreshedStaleDiagnostics(t *testing.T) {
	ctx := context.Background()
	t.Run("query failure", func(t *testing.T) {
		catalogPath := filepath.Join(t.TempDir(), "catalog.db")
		root := makeRepo(t, "query-failure")
		entries := register(t, catalogPath, root)
		writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
			Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
			Symbols: []graph.Symbol{fixtureSymbol("unit", "symbol-id", "Symbol", "query")},
		})
		store, err := federation.Open(ctx, catalogPath, nil, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		queryCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, _, _ = store.FindSymbols(queryCtx, "Symbol", 4)
		if !store.Partial() {
			t.Fatal("member query failure did not mark federation partial")
		}
	})

	t.Run("stale diagnostic with successful refresh", func(t *testing.T) {
		catalogPath := filepath.Join(t.TempDir(), "catalog.db")
		root := makeRepo(t, "stale-refreshed")
		entries := register(t, catalogPath, root)
		writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
			Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
			Symbols: []graph.Symbol{fixtureSymbol("unit", "symbol-id", "Symbol", "stale")},
		})
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		refreshed := 0
		store, err := federation.OpenFresh(ctx, catalogPath, nil, 4, func(context.Context, string) error {
			refreshed++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if refreshed != 1 || store.Partial() {
			t.Fatalf("refreshed=%d partial=%t diagnostics=%q", refreshed, store.Partial(), store.Diagnostics())
		}
		if diagnostics := strings.Join(store.Diagnostics(), "\n"); !strings.Contains(diagnostics, "stale catalog state") {
			t.Fatalf("diagnostics = %q", diagnostics)
		}
	})
}

func TestOpenFreshRefreshesSelectedMembersAndExcludesFailures(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	firstRoot := makeRepo(t, "fresh-first")
	secondRoot := makeRepo(t, "fresh-second")
	entries := register(t, catalogPath, firstRoot, secondRoot)
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{Unit: graph.Unit{ID: "first", Provider: "fixture", ProviderVersion: "1"}, Symbols: []graph.Symbol{fixtureSymbol("first", "fresh-symbol", "FreshSymbol", "fresh")}})
	var refreshed []string
	store, err := federation.OpenFresh(ctx, catalogPath, nil, 8, func(ctx context.Context, root string) error {
		refreshed = append(refreshed, root)
		if strings.HasSuffix(filepath.ToSlash(root), "/fresh-second") {
			return fmt.Errorf("fixture compiler unavailable")
		}
		_, err := (&freshness.Manager{Directory: root, Provider: freshness.EmptyProvider{}, Command: "test"}).Ensure(ctx, false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.Partial() {
		t.Fatal("excluded refresh failure did not mark federation partial")
	}
	if len(refreshed) != 2 {
		t.Fatalf("refreshed = %q", refreshed)
	}
	values, _, err := store.FindSymbols(ctx, "FreshSymbol", 10)
	if err != nil || len(values) != 1 {
		t.Fatalf("healthy values = %#v, %v", values, err)
	}
	if diagnostics := strings.Join(store.Diagnostics(), "\n"); !strings.Contains(diagnostics, "excluded: refresh failed: fixture compiler unavailable") {
		t.Fatalf("diagnostics = %q", diagnostics)
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
