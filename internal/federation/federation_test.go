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

func TestMachineAggregateMatchesFederationAndInvalidatesByGeneration(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	cacheDirectory := filepath.Join(filepath.Dir(catalogPath), "aggregate")
	firstRoot := makeRepo(t, "aggregate-first")
	secondRoot := makeRepo(t, "aggregate-second")
	entries := register(t, catalogPath, firstRoot, secondRoot)
	target := "shared-target"
	caller := "first-caller"
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
		Unit: graph.Unit{ID: "first", Provider: "fixture", ProviderVersion: "1", InventoryDigest: "first-v1"},
		Symbols: []graph.Symbol{
			fixtureSymbol("first", caller, "Caller", "caller"),
			fixtureSymbol("first", target, "SharedTarget", "zz"),
		},
		Edges: []graph.Edge{{ID: "call-v1", UnitID: "first", From: caller, To: target, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"}},
	})
	writeFacts(t, entries[1].DatabasePath, graph.UnitFacts{
		Unit:    graph.Unit{ID: "second", Provider: "fixture", ProviderVersion: "1", InventoryDigest: "second-v1"},
		Symbols: []graph.Symbol{fixtureSymbol("second", target, "SharedTarget", "target")},
		Edges:   []graph.Edge{{ID: "call-second", UnitID: "second", From: caller, To: target, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"}},
	})

	authoritative, err := federation.Open(ctx, catalogPath, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	wantSymbols, wantTruncated, err := authoritative.FindSymbols(ctx, "SharedTarget", 10)
	if err != nil {
		t.Fatal(err)
	}
	wantEdges, wantEdgeTruncated, err := authoritative.EdgesTo(ctx, target, []graph.EdgeKind{graph.EdgeCalls}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := authoritative.Close(); err != nil {
		t.Fatal(err)
	}

	generations := map[string]string{entries[0].Root: "first-generation-v1", entries[1].Root: "second-generation-v1"}
	for _, entry := range entries {
		markGeneration(t, entry.DatabasePath, generations[entry.Root])
	}
	refresh := func(_ context.Context, root string) (string, error) { return generations[root], nil }
	accelerated, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if !accelerated.Accelerated() || !accelerated.CacheStatus().Rebuilt {
		t.Fatalf("first accelerated status = %#v, diagnostics=%q", accelerated.CacheStatus(), accelerated.Diagnostics())
	}
	gotSymbols, gotTruncated, err := accelerated.FindSymbols(ctx, "SharedTarget", 10)
	if err != nil || gotTruncated != wantTruncated || !reflect.DeepEqual(gotSymbols, wantSymbols) {
		t.Fatalf("symbols got=%#v,%t,%v want=%#v,%t", gotSymbols, gotTruncated, err, wantSymbols, wantTruncated)
	}
	gotEdges, gotEdgeTruncated, err := accelerated.EdgesTo(ctx, target, []graph.EdgeKind{graph.EdgeCalls}, 10)
	if err != nil || gotEdgeTruncated != wantEdgeTruncated || !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Fatalf("edges got=%#v,%t,%v want=%#v,%t", gotEdges, gotEdgeTruncated, err, wantEdges, wantEdgeTruncated)
	}
	if sources := accelerated.Sources(); len(sources) != 4 || sources[0].Kind != "edge" || sources[1].Kind != "edge" || sources[2].Kind != "symbol" || sources[3].Kind != "symbol" {
		t.Fatalf("accelerated sources = %#v", sources)
	}
	firstCache := accelerated.CacheStatus().Path
	if err := accelerated.Close(); err != nil {
		t.Fatal(err)
	}

	// An exact cache hit does not open the worktree databases again.
	for _, entry := range entries {
		if err := os.Rename(entry.DatabasePath, entry.DatabasePath+".held"); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(entry.DatabasePath+".held", entry.DatabasePath)
	}
	hit, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Accelerated() || hit.CacheStatus().Rebuilt || hit.CacheStatus().Path != firstCache {
		t.Fatalf("cache hit = %#v diagnostics=%q", hit.CacheStatus(), hit.Diagnostics())
	}
	if values, _, err := hit.FindSymbols(ctx, "SharedTarget", 10); err != nil || len(values) != 1 {
		t.Fatalf("cache hit query = %#v, %v", values, err)
	}
	hit.Close()
	for _, entry := range entries {
		if err := os.Rename(entry.DatabasePath+".held", entry.DatabasePath); err != nil {
			t.Fatal(err)
		}
	}

	// Changing one authoritative generation makes the old aggregate unusable
	// and materializes a distinct complete generation.
	generations[entries[0].Root] = "first-generation-v2"
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
		Unit: graph.Unit{ID: "first", Provider: "fixture", ProviderVersion: "1", InventoryDigest: "first-v2"},
		Symbols: []graph.Symbol{
			fixtureSymbol("first", caller, "Caller", "caller"),
			fixtureSymbol("first", "new-symbol", "NewGlobalSymbol", "new"),
		},
		Edges: []graph.Edge{{ID: "call-v2", UnitID: "first", From: caller, To: target, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"}},
	})
	markGeneration(t, entries[0].DatabasePath, generations[entries[0].Root])
	changed, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Close()
	if !changed.Accelerated() || !changed.CacheStatus().Rebuilt || changed.CacheStatus().Path == firstCache {
		t.Fatalf("changed cache = %#v diagnostics=%q", changed.CacheStatus(), changed.Diagnostics())
	}
	if values, _, err := changed.FindSymbols(ctx, "NewGlobalSymbol", 10); err != nil || len(values) != 1 || values[0].ID != "new-symbol" {
		t.Fatalf("changed query = %#v, %v", values, err)
	}
}

func TestMachineAggregateNeverServesMissingWorktree(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	cacheDirectory := filepath.Join(filepath.Dir(catalogPath), "aggregate")
	root := makeRepo(t, "aggregate-missing")
	entries := register(t, catalogPath, root)
	writeFacts(t, entries[0].DatabasePath, graph.UnitFacts{
		Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{fixtureSymbol("unit", "cached", "Cached", "cached")},
	})
	markGeneration(t, entries[0].DatabasePath, "generation")
	refresh := func(context.Context, string) (string, error) { return "generation", nil }
	store, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 4, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.Rename(root, root+"-missing"); err != nil {
		t.Fatal(err)
	}
	missing, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 4, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	if missing.Accelerated() || !missing.Partial() {
		t.Fatalf("missing store accelerated=%t partial=%t diagnostics=%q", missing.Accelerated(), missing.Partial(), missing.Diagnostics())
	}
	values, _, err := missing.FindSymbols(ctx, "Cached", 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("missing worktree leaked cached result %#v, %v", values, err)
	}
}

func TestBusyMachineAggregateReaderFallsBackToCompleteAuthority(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	cacheDirectory := filepath.Join(filepath.Dir(catalogPath), "aggregate")
	root := makeRepo(t, "aggregate-busy-reader")
	entry := register(t, catalogPath, root)[0]
	writeFacts(t, entry.DatabasePath, graph.UnitFacts{
		Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{fixtureSymbol("unit", "old", "OldCachedFact", "old")},
	})
	const generation = "busy-generation"
	markGeneration(t, entry.DatabasePath, generation)
	refresh := func(context.Context, string) (string, error) { return generation, nil }
	reader, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 4, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if !reader.Accelerated() {
		t.Fatalf("fixture aggregate was not built: diagnostics=%q", reader.Diagnostics())
	}

	// Deliberately retain the same fixture generation while replacing authority
	// so an accidental read from the busy aggregate is immediately observable.
	writeFacts(t, entry.DatabasePath, graph.UnitFacts{
		Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{fixtureSymbol("unit", "new", "NewAuthoritativeFact", "new")},
	})
	markGeneration(t, entry.DatabasePath, generation)
	fallback, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 4, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Close()
	if fallback.Accelerated() || fallback.Partial() {
		t.Fatalf("busy fallback accelerated=%t partial=%t diagnostics=%q", fallback.Accelerated(), fallback.Partial(), fallback.Diagnostics())
	}
	values, truncated, err := fallback.FindSymbols(ctx, "NewAuthoritativeFact", 10)
	if err != nil || truncated || len(values) != 1 || values[0].ID != "new" {
		t.Fatalf("authoritative values=%#v truncated=%t error=%v diagnostics=%q", values, truncated, err, fallback.Diagnostics())
	}
	if values, _, err := fallback.FindSymbols(ctx, "OldCachedFact", 10); err != nil || len(values) != 0 {
		t.Fatalf("busy aggregate leaked stale values=%#v error=%v", values, err)
	}
	diagnostics := strings.Join(fallback.Diagnostics(), "\n")
	if !strings.Contains(diagnostics, "machine aggregate unavailable; using authoritative federation") || !strings.Contains(diagnostics, "busy") {
		t.Fatalf("busy fallback diagnostics=%q", diagnostics)
	}
}

func TestMachineAggregateGenerationChangeDuringScanFallsBackToNewAuthority(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	cacheDirectory := filepath.Join(filepath.Dir(catalogPath), "aggregate")
	root := makeRepo(t, "aggregate-racing-refresh")
	entry := register(t, catalogPath, root)[0]
	writeFacts(t, entry.DatabasePath, graph.UnitFacts{
		Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{fixtureSymbol("unit", "old", "OldGeneration", "old")},
	})
	markGeneration(t, entry.DatabasePath, "generation-1")
	calls := 0
	refresh := func(context.Context, string) (string, error) {
		calls++
		if calls == 2 {
			// This runs only after the aggregate released its source handle. It
			// models a concurrent authoritative refresh winning during the scan.
			writeFacts(t, entry.DatabasePath, graph.UnitFacts{
				Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
				Symbols: []graph.Symbol{fixtureSymbol("unit", "new", "NewGeneration", "new")},
			})
			markGeneration(t, entry.DatabasePath, "generation-2")
		}
		if calls >= 2 {
			return "generation-2", nil
		}
		return "generation-1", nil
	}
	store, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 4, cacheDirectory, refresh)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Accelerated() || calls < 3 {
		t.Fatalf("accelerated=%t refresh_calls=%d diagnostics=%q", store.Accelerated(), calls, store.Diagnostics())
	}
	values, _, err := store.FindSymbols(ctx, "NewGeneration", 10)
	if err != nil || len(values) != 1 || values[0].ID != "new" {
		t.Fatalf("authoritative fallback values=%#v error=%v diagnostics=%q", values, err, store.Diagnostics())
	}
	if values, _, err := store.FindSymbols(ctx, "OldGeneration", 10); err != nil || len(values) != 0 {
		t.Fatalf("mislabeled old values=%#v error=%v", values, err)
	}
}

func BenchmarkFederatedVsMachineAggregateSearch(b *testing.B) {
	catalogPath, cacheDirectory, refresh := benchmarkAggregateFixture(b)
	ctx := context.Background()
	authoritative, err := federation.Open(ctx, catalogPath, nil, 8)
	if err != nil {
		b.Fatal(err)
	}
	defer authoritative.Close()
	accelerated, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
	if err != nil {
		b.Fatal(err)
	}
	defer accelerated.Close()
	if !accelerated.Accelerated() {
		b.Fatal("aggregate was not reused")
	}
	for name, store := range map[string]*federation.Store{"federated": authoritative, "aggregate": accelerated} {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				values, _, err := store.FindSymbols(ctx, "ServiceHandler009", 20)
				if err != nil || len(values) == 0 {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFederatedVsMachineAggregateOpenAndSearch(b *testing.B) {
	catalogPath, cacheDirectory, refresh := benchmarkAggregateFixture(b)
	ctx := context.Background()
	for name, open := range map[string]func() (*federation.Store, error){
		"federated": func() (*federation.Store, error) { return federation.Open(ctx, catalogPath, nil, 8) },
		"aggregate": func() (*federation.Store, error) {
			return federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
		},
	} {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				store, err := open()
				if err != nil {
					b.Fatal(err)
				}
				values, _, queryErr := store.FindSymbols(ctx, "ServiceHandler009", 20)
				closeErr := store.Close()
				if queryErr != nil || closeErr != nil || len(values) == 0 {
					b.Fatalf("query=%v close=%v values=%d", queryErr, closeErr, len(values))
				}
			}
		})
	}
}

func BenchmarkFederatedVsMachineAggregateTraversal(b *testing.B) {
	catalogPath, cacheDirectory, refresh := benchmarkAggregateFixture(b)
	ctx := context.Background()
	authoritative, err := federation.Open(ctx, catalogPath, nil, 8)
	if err != nil {
		b.Fatal(err)
	}
	defer authoritative.Close()
	accelerated, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
	if err != nil {
		b.Fatal(err)
	}
	defer accelerated.Close()
	for name, store := range map[string]*federation.Store{"federated": authoritative, "aggregate": accelerated} {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				edges, _, err := store.EdgesTo(ctx, "benchmark-target", []graph.EdgeKind{graph.EdgeCalls}, 20)
				if err != nil || len(edges) != 1 {
					b.Fatalf("edges=%d error=%v", len(edges), err)
				}
			}
		})
	}
}

func benchmarkAggregateFixture(b *testing.B) (string, string, federation.GenerationRefresher) {
	b.Helper()
	ctx := context.Background()
	catalogPath := filepath.Join(b.TempDir(), "catalog.db")
	cacheDirectory := filepath.Join(filepath.Dir(catalogPath), "aggregate")
	var roots []string
	for i := 0; i < 8; i++ {
		roots = append(roots, makeRepo(b, fmt.Sprintf("benchmark-%d", i)))
	}
	entries := register(b, catalogPath, roots...)
	for member, entry := range entries {
		facts := graph.UnitFacts{Unit: graph.Unit{ID: fmt.Sprintf("unit-%d", member), Provider: "fixture", ProviderVersion: "1"}}
		for i := 0; i < 625; i++ {
			id := fmt.Sprintf("member-%d-symbol-%04d", member, i)
			facts.Symbols = append(facts.Symbols, fixtureSymbol(facts.Unit.ID, id, fmt.Sprintf("ServiceHandler%04d", i), id))
		}
		facts.Edges = append(facts.Edges, graph.Edge{ID: fmt.Sprintf("benchmark-edge-%d", member), UnitID: facts.Unit.ID, From: "benchmark-caller", To: "benchmark-target", Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"})
		writeFacts(b, entry.DatabasePath, facts)
		markGeneration(b, entry.DatabasePath, "generation:"+entry.Root)
	}
	refresh := func(_ context.Context, root string) (string, error) { return "generation:" + root, nil }
	built, err := federation.OpenFreshAccelerated(ctx, catalogPath, nil, 8, cacheDirectory, refresh)
	if err != nil {
		b.Fatal(err)
	}
	built.Close()
	return catalogPath, cacheDirectory, refresh
}

func fixtureSymbol(unit, id, name, suffix string) graph.Symbol {
	return graph.Symbol{ID: id, UnitID: unit, StableName: id, DisplayName: name, Kind: "function", Provider: "fixture", Evidence: graph.EvidenceExact, DocumentID: "", Definition: graph.Range{}, NormalizedName: strings.ToLower(name + suffix)}
}

func register(t testing.TB, path string, roots ...string) []catalog.Entry {
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

func writeFacts(t testing.TB, path string, facts graph.UnitFacts) {
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

func markGeneration(t testing.TB, path, generation string) {
	t.Helper()
	db, err := storage.Open(context.Background(), path, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetGeneration(context.Background(), generation); err != nil {
		t.Fatal(err)
	}
}

func makeRepo(t testing.TB, name string) string {
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

func runGit(t testing.TB, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
