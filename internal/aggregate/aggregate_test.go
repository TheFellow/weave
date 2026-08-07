package aggregate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/aggregate"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
)

type fixtureSource struct {
	symbols []graph.Symbol
	edges   []graph.Edge
	err     error
	mu      sync.Mutex
	scans   int
}

func (source *fixtureSource) ScanHotFacts(ctx context.Context, symbol func(graph.Symbol) error, edge func(graph.Edge) error) error {
	source.mu.Lock()
	source.scans++
	source.mu.Unlock()
	if source.err != nil {
		return source.err
	}
	for _, value := range source.symbols {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := symbol(value); err != nil {
			return err
		}
	}
	for _, value := range source.edges {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := edge(value); err != nil {
			return err
		}
	}
	return nil
}

func (source *fixtureSource) scanCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.scans
}

func TestEnsureDeterministicQueryEquivalentAndProvenance(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	target := symbol("target", "Target")
	caller := symbol("caller", "CallTarget")
	call := graph.Edge{ID: "call", UnitID: "unit", From: caller.ID, To: target.ID, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"}
	firstStore := &fixtureSource{symbols: []graph.Symbol{caller, target}, edges: []graph.Edge{call}}
	secondStore := &fixtureSource{symbols: []graph.Symbol{target}}
	first := source("b", "repo/b", "generation-b", firstStore)
	second := source("a", "repo/a", "generation-a", secondStore)

	db, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{first, second}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Rebuilt || status.Sources != 2 || status.Symbols != 2 || status.Edges != 1 || status.Bytes == 0 {
		t.Fatalf("status = %#v", status)
	}
	values, truncated, err := db.FindSymbols(ctx, "Target", 10)
	if err != nil || truncated || len(values) != 2 || values[0].ID != target.ID {
		t.Fatalf("FindSymbols = %#v, %t, %v", values, truncated, err)
	}
	edges, truncated, err := db.EdgesTo(ctx, target.ID, []graph.EdgeKind{graph.EdgeCalls}, 10)
	if err != nil || truncated || !reflect.DeepEqual(edges, []graph.Edge{call}) {
		t.Fatalf("EdgesTo = %#v, %t, %v", edges, truncated, err)
	}
	sources := db.Sources()
	if len(sources) != 4 || sources[0].FactID != "call" || sources[1].FactID != "caller" || sources[2].FactID != "target" || sources[3].FactID != "target" || sources[2].Repository != "repo/a" || sources[3].Repository != "repo/b" {
		t.Fatalf("sources = %#v", sources)
	}
	path := db.Path()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Source order and Store implementation identity do not affect generation.
	generation, secondPath, err := aggregate.Generation(directory, []aggregate.Source{second, first})
	if err != nil || generation != status.Generation || secondPath != path {
		t.Fatalf("Generation = %q, %q, %v; status=%#v", generation, secondPath, err, status)
	}
	cached, cachedStatus, err := aggregate.Open(ctx, directory, []aggregate.Source{
		withoutCallbacks(source("a", "repo/a", "generation-a", nil)),
		withoutCallbacks(source("b", "repo/b", "generation-b", nil)),
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cached.Close()
	if cachedStatus.Rebuilt || firstStore.scanCount() != 1 || secondStore.scanCount() != 1 {
		t.Fatalf("cache hit status=%#v scans=%d,%d", cachedStatus, firstStore.scanCount(), secondStore.scanCount())
	}
}

func TestEnsureRequiresPostScanValidationOnlyWhenRebuilding(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	value := source("repo", "repo", "generation", &fixtureSource{symbols: []graph.Symbol{symbol("value", "Value")}})
	value.Validate = nil
	if _, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{value}, time.Second); !errors.Is(err, aggregate.ErrInvalid) || !strings.Contains(err.Error(), "post-scan generation validator") {
		t.Fatalf("Ensure without validator error = %v", err)
	}
	_, path, err := aggregate.Generation(directory, []aggregate.Source{value})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unvalidated build published %q: %v", path, err)
	}

	value.Validate = func(context.Context) (string, error) { return value.Generation, nil }
	db, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{value}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	value = withoutCallbacks(value)
	cached, _, err := aggregate.Open(ctx, directory, []aggregate.Source{value}, time.Second)
	if err != nil {
		t.Fatalf("Open required rebuild callbacks: %v", err)
	}
	cached.Close()
}

func TestStatusCountsDistinctFactsAcrossSourceVariants(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	shared := symbol("shared", "Shared")
	edgeA := graph.Edge{ID: "edge-a", UnitID: "unit-a", From: "caller", To: shared.ID, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "fixture"}
	edgeB := edgeA
	edgeB.ID, edgeB.UnitID = "edge-b", "unit-b"
	first := source("a", "repo/a", "generation-a", &fixtureSource{symbols: []graph.Symbol{shared}, edges: []graph.Edge{edgeA}})
	second := source("b", "repo/b", "generation-b", &fixtureSource{symbols: []graph.Symbol{shared}, edges: []graph.Edge{edgeB}})
	db, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{first, second}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if status.Symbols != 1 || status.Edges != 1 {
		t.Fatalf("distinct status = %#v", status)
	}
}

func TestGenerationChangeAndFailedRebuildPreservePublishedCache(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	store := &fixtureSource{symbols: []graph.Symbol{symbol("old", "Old")}}
	oldSource := source("repo", "repo", "generation-1", store)
	db, oldStatus, err := aggregate.Ensure(ctx, directory, []aggregate.Source{oldSource}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	failed := source("repo", "repo", "generation-2", &fixtureSource{err: errors.New("interrupted source scan")})
	if _, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{failed}, time.Second); err == nil {
		t.Fatal("failed rebuild unexpectedly succeeded")
	}
	if _, err := os.Stat(oldStatus.Path); err != nil {
		t.Fatalf("previous published cache was not preserved: %v", err)
	}
	old, _, err := aggregate.Open(ctx, directory, []aggregate.Source{oldSource}, time.Second)
	if err != nil {
		t.Fatalf("previous cache no longer opens: %v", err)
	}
	old.Close()

	newStore := &fixtureSource{symbols: []graph.Symbol{symbol("new", "New")}}
	newSource := source("repo", "repo", "generation-2", newStore)
	current, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{newSource}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if status.Generation == oldStatus.Generation || status.Path == oldStatus.Path {
		t.Fatalf("generation did not change: old=%#v new=%#v", oldStatus, status)
	}
	if values, _, err := current.FindSymbols(ctx, "New", 1); err != nil || len(values) != 1 {
		t.Fatalf("new query = %#v, %v", values, err)
	}
}

func TestMaterializationRejectsGenerationChangedDuringScan(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	baseline := source("repo", "repo", "generation-1", &fixtureSource{symbols: []graph.Symbol{symbol("old", "Old")}})
	db, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{baseline}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	released := false
	changing := source("repo", "repo", "generation-2", &fixtureSource{symbols: []graph.Symbol{symbol("mixed", "Mixed")}})
	changing.Release = func() error { released = true; return nil }
	changing.Validate = func(context.Context) (string, error) {
		if !released {
			t.Fatal("generation was validated before the source handle was released")
		}
		return "generation-3", nil
	}
	if _, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{changing}, time.Second); !errors.Is(err, aggregate.ErrStale) {
		t.Fatalf("changed-during-scan error = %v", err)
	}
	if _, err := os.Stat(status.Path); err != nil {
		t.Fatalf("previous generation was not preserved: %v", err)
	}
	_, changedPath, err := aggregate.Generation(directory, []aggregate.Source{changing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(changedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mislabeled generation was published: %v", err)
	}
}

func TestCorruptGenerationIsRebuiltAndBounded(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	store := &fixtureSource{}
	for i := 0; i < 12; i++ {
		store.symbols = append(store.symbols, symbol(fmt.Sprintf("symbol-%02d", i), fmt.Sprintf("Match%02d", i)))
	}
	source := source("repo", "repo", "generation", store)
	db, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(status.Path, []byte("not a bstore database"), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt, rebuiltStatus, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	if !rebuiltStatus.Rebuilt || store.scanCount() != 2 {
		t.Fatalf("rebuilt=%#v scans=%d", rebuiltStatus, store.scanCount())
	}
	values, truncated, err := rebuilt.FindSymbols(ctx, "Match", 3)
	if err != nil || !truncated || len(values) != 3 {
		t.Fatalf("bounded search = %#v, %t, %v", values, truncated, err)
	}
}

func TestConcurrentEnsurePublishesOneGeneration(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	store := &fixtureSource{symbols: []graph.Symbol{symbol("shared", "Shared")}}
	source := source("repo", "repo", "generation", store)
	start := make(chan struct{})
	type result struct {
		status aggregate.Status
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			db, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source}, 5*time.Second)
			if db != nil {
				db.Close()
			}
			results <- result{status, err}
		}()
	}
	close(start)
	got := []result{<-results, <-results}
	for _, value := range got {
		if value.err != nil {
			t.Fatalf("concurrent Ensure: %v", value.err)
		}
	}
	if got[0].status.Generation != got[1].status.Generation || got[0].status.Path != got[1].status.Path {
		t.Fatalf("concurrent statuses = %#v", got)
	}
	if store.scanCount() != 1 {
		t.Fatalf("source scanned %d times, want 1", store.scanCount())
	}
}

func TestOpenReaderContentionDoesNotRebuildPublishedGeneration(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "aggregate")
	store := &fixtureSource{symbols: []graph.Symbol{symbol("shared", "Shared")}}
	source := source("repo", "repo", "generation", store)
	reader, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	started := time.Now()
	if _, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source}, 20*time.Millisecond); !errors.Is(err, aggregate.ErrBusy) {
		t.Fatalf("contended Ensure error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("contention was not bounded: %v", time.Since(started))
	}
	if store.scanCount() != 1 {
		t.Fatalf("contended reader triggered rebuild; scans=%d", store.scanCount())
	}
}

func TestHotProjectionIsSmallerThanVerboseWorktreeFixture(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	localPath := filepath.Join(root, "index.db")
	local, err := storage.Open(ctx, localPath, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	facts := graph.UnitFacts{Unit: graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1", InventoryDigest: "large"}}
	for i := 0; i < 1_600; i++ {
		document := graph.Document{ID: fmt.Sprintf("doc-%04d", i), UnitID: "unit", Path: fmt.Sprintf("docs/%04d-%s.md", i, strings.Repeat("x", 512)), Language: "markdown", Provider: "fixture", ProviderVersion: "1"}
		symbol := symbol(fmt.Sprintf("symbol-%04d", i), fmt.Sprintf("Symbol%04d", i))
		symbol.UnitID, symbol.DocumentID = "unit", document.ID
		facts.Documents = append(facts.Documents, document)
		facts.Symbols = append(facts.Symbols, symbol)
		facts.Occurrences = append(facts.Occurrences, graph.Occurrence{ID: fmt.Sprintf("occurrence-%04d", i), UnitID: "unit", SymbolID: symbol.ID, DocumentID: document.ID, Role: "definition", Provider: "fixture", Evidence: graph.EvidenceExact})
	}
	if err := local.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	localInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "aggregate")
	db, status, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source("repo", "repo", "generation", local)}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	local.Close()
	if status.Symbols != 1_600 || status.Edges != 0 || status.Bytes >= localInfo.Size() {
		t.Fatalf("projection status=%#v local_bytes=%d", status, localInfo.Size())
	}
	t.Logf("local=%d aggregate=%d ratio=%.3f", localInfo.Size(), status.Bytes, float64(status.Bytes)/float64(localInfo.Size()))
}

func TestGenerationRejectsInvalidAndDuplicateSources(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "aggregate")
	valid := source("repo", "repo", "generation", &fixtureSource{})
	if _, _, err := aggregate.Generation("relative", []aggregate.Source{valid}); err == nil {
		t.Fatal("relative aggregate directory accepted")
	}
	if _, _, err := aggregate.Generation(directory, []aggregate.Source{valid, valid}); err == nil {
		t.Fatal("duplicate source accepted")
	}
	if _, _, err := aggregate.Generation(directory, nil); err == nil {
		t.Fatal("empty sources accepted")
	}
}

func TestCanceledBuildPublishesNothing(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "aggregate")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := source("repo", "repo", "generation", &fixtureSource{symbols: []graph.Symbol{symbol("value", "Value")}})
	if _, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source}, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Ensure error = %v", err)
	}
	_, path, err := aggregate.Generation(directory, []aggregate.Source{source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled build published %q: %v", path, err)
	}
}

func BenchmarkHotAggregateSearch(b *testing.B) {
	ctx := context.Background()
	store := &fixtureSource{}
	for i := 0; i < 10_000; i++ {
		store.symbols = append(store.symbols, symbol(fmt.Sprintf("symbol-%05d", i), fmt.Sprintf("ServiceHandler%05d", i)))
	}
	directory := filepath.Join(b.TempDir(), "aggregate")
	db, _, err := aggregate.Ensure(ctx, directory, []aggregate.Source{source("repo", "repo", "generation", store)}, 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	b.ResetTimer()
	for range b.N {
		values, _, err := db.FindSymbols(ctx, "ServiceHandler099", 20)
		if err != nil || len(values) == 0 {
			b.Fatal(err)
		}
	}
}

func source(key, repository, generation string, store aggregate.HotSource) aggregate.Source {
	return aggregate.Source{
		Key: key, Repository: repository, WorktreeID: "main", Root: filepath.Join(string(filepath.Separator), "repos", key),
		DatabasePath: filepath.Join(string(filepath.Separator), "repos", key, "index.db"), Generation: generation, Store: store,
		Validate: func(context.Context) (string, error) { return generation, nil },
	}
}

func withoutCallbacks(source aggregate.Source) aggregate.Source {
	source.Store = nil
	source.Release = nil
	source.Validate = nil
	return source
}

func symbol(id, name string) graph.Symbol {
	return graph.Symbol{ID: id, UnitID: "unit", StableName: id, DisplayName: name, NormalizedName: graph.NormalizeName(name), Kind: "function", Provider: "fixture", Evidence: graph.EvidenceExact}
}
