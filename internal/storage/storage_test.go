package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/mjl-/bstore"
)

func TestOpenCreateAndMustExist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "index.db")

	if _, err := Open(ctx, path, Options{MustExist: true}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open(MustExist) error = %v, want not exist", err)
	}
	db, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path, Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
}

func TestOpenRejectsUnsupportedSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db := openTestDB(t, path)
	if err := db.db.Update(ctx, &metadataRecord{ID: 1, Version: StorageSchemaVersion + 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, path, Options{MustExist: true})
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("Open() error = %v, want ErrSchema", err)
	}
}

func TestOpenClassifiesCorruptFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(path, []byte("not a bolt database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, Options{MustExist: true})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open() error = %v, want ErrCorrupt", err)
	}
}

func TestReplaceUnitIsAtomicAndReplacesOwnedFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()

	initial := fixtureFacts("unit-a")
	if err := db.ReplaceUnit(ctx, initial); err != nil {
		t.Fatal(err)
	}

	conflict := fixtureFacts("unit-b")
	conflict.Symbols[0].ID = initial.Symbols[0].ID // Failure occurs after unit-b and its document were staged.
	conflict.Occurrences = nil
	conflict.Edges = nil
	if err := db.ReplaceUnit(ctx, conflict); err == nil {
		t.Fatal("ReplaceUnit(conflict) error = nil")
	}
	snapshot, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(snapshot.Units), 1; got != want {
		t.Fatalf("units after rollback = %d, want %d", got, want)
	}
	if snapshot.Units[0].ID != "unit-a" {
		t.Fatalf("unit after rollback = %q", snapshot.Units[0].ID)
	}

	replacement := initial
	replacement.Symbols = replacement.Symbols[:1]
	replacement.Occurrences = nil
	replacement.Edges = nil
	if err := db.ReplaceUnit(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	snapshot, err = db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Symbols) != 1 || len(snapshot.Occurrences) != 0 || len(snapshot.Edges) != 0 {
		t.Fatalf("replacement retained old facts: %#v", snapshot)
	}
}

func TestIncrementalReplacementValidatesAllBatchesBeforeWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	if err := db.ReplaceUnit(ctx, fixtureFacts("old")); err != nil {
		t.Fatal(err)
	}
	first, second := fixtureFacts("first"), fixtureFacts("second")
	second.Symbols[0].ID = first.Symbols[0].ID
	if err := db.ReplaceUnitsIncremental(ctx, []graph.UnitFacts{first, second}, []string{"old"}, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incremental conflict error = %v", err)
	}
	snapshot, err := db.Export(ctx)
	if err != nil || len(snapshot.Units) != 1 || snapshot.Units[0].ID != "old" {
		t.Fatalf("prevalidated replacement changed database: %#v, %v", snapshot, err)
	}
}

func TestBidirectionalAdjacencyAndSymbolSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	facts := fixtureFacts("unit-a")
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}

	forward, truncated, err := db.EdgesFrom(ctx, facts.Symbols[0].ID, []graph.EdgeKind{graph.EdgeCalls}, 10)
	if err != nil || truncated {
		t.Fatalf("EdgesFrom() = %#v, %v, %v", forward, truncated, err)
	}
	if len(forward) != 1 || forward[0].To != facts.Symbols[1].ID {
		t.Fatalf("EdgesFrom() = %#v", forward)
	}
	reverse, truncated, err := db.EdgesTo(ctx, facts.Symbols[1].ID, []graph.EdgeKind{graph.EdgeCalls}, 10)
	if err != nil || truncated {
		t.Fatalf("EdgesTo() = %#v, %v, %v", reverse, truncated, err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("reverse adjacency = %#v, want %#v", reverse, forward)
	}

	for _, query := range []string{"hand", "http", "request"} {
		results, truncated, err := db.FindSymbols(ctx, query, 10)
		if err != nil || truncated {
			t.Fatalf("FindSymbols(%q) = %#v, %v, %v", query, results, truncated, err)
		}
		if len(results) != 1 || results[0].DisplayName != "HandleHTTPRequest" {
			t.Errorf("FindSymbols(%q) = %#v", query, results)
		}
	}
}

func TestExportIsDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var snapshots []graph.Snapshot
	for i := 0; i < 2; i++ {
		db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
		facts := fixtureFacts("unit-a")
		if i == 1 {
			slices.Reverse(facts.Documents)
			slices.Reverse(facts.Symbols)
			slices.Reverse(facts.Occurrences)
			slices.Reverse(facts.Edges)
		}
		if err := db.ReplaceUnit(ctx, facts); err != nil {
			t.Fatal(err)
		}
		snapshot, err := db.Export(ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
		db.Close()
	}
	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatalf("exports differ:\n%#v\n%#v", snapshots[0], snapshots[1])
	}
}

func TestVerifyFindsLogicalDamage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	if err := db.ReplaceUnit(ctx, fixtureFacts("unit-a")); err != nil {
		t.Fatal(err)
	}
	facts := fixtureFacts("unit-a")
	facts.Occurrences[0].SymbolID = "missing"
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	issues, err := db.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Kind != "unresolved-occurrence" || issues[0].Severity != IssueWarning || issues[0].Fatal() {
		t.Fatalf("Verify() = %#v", issues)
	}
}

func TestVerifyClassifiesOwnershipDamageAsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	if err := db.ReplaceUnit(ctx, fixtureFacts("unit-a")); err != nil {
		t.Fatal(err)
	}
	bad, err := bstore.QueryDB[documentRecord](ctx, db.db).FilterEqual("StableID", "unit-a:main.go").Get()
	if err != nil {
		t.Fatal(err)
	}
	bad.Unit = ^uint64(0)
	if err := db.db.Update(ctx, &bad); err != nil {
		t.Fatal(err)
	}
	issues, err := db.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Kind != "orphan-document" || issues[0].Severity != IssueError || !issues[0].Fatal() {
		t.Fatalf("Verify() = %#v", issues)
	}
}

func TestCompactPreservesFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db := openTestDB(t, path)
	facts := fixtureFacts("unit-a")
	facts.Unit.InventoryDigest = strings.Repeat("x", 256<<10)
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	facts.Unit.InventoryDigest = "small"
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	want, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() > before.Size() {
		t.Errorf("compacted size = %d, before %d", after.Size(), before.Size())
	}
	db = openTestDB(t, path)
	defer db.Close()
	got, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("export after compact differs: %#v", got)
	}
}

func TestValidationRejectsMalformedFactsBeforeWrite(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	facts := fixtureFacts("unit-a")
	facts.Edges[0].Kind = "magic"
	err := db.ReplaceUnit(context.Background(), facts)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ReplaceUnit() error = %v, want ErrInvalid", err)
	}
	snapshot, exportErr := db.Export(context.Background())
	if exportErr != nil {
		t.Fatal(exportErr)
	}
	if len(snapshot.Units) != 0 {
		t.Fatalf("invalid facts changed store: %#v", snapshot)
	}
}

func openTestDB(t testing.TB, path string) *DB {
	t.Helper()
	db, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func fixtureFacts(unit string) graph.UnitFacts {
	docID := unit + ":main.go"
	caller := unit + ":HandleHTTPRequest"
	callee := unit + ":authorize"
	rng := graph.Range{Start: graph.Position{Line: 2, Column: 1, Byte: 20}, End: graph.Position{Line: 2, Column: 8, Byte: 27}}
	return graph.UnitFacts{
		Unit:      graph.Unit{ID: unit, Provider: "fixture", ProviderVersion: "1.0.0", Language: "go"},
		Documents: []graph.Document{{ID: docID, UnitID: unit, Path: "main.go", Language: "go", ContentHash: "sha256:abc", Provider: "fixture", ProviderVersion: "1.0.0"}},
		Symbols: []graph.Symbol{
			{ID: caller, UnitID: unit, StableName: "example.HandleHTTPRequest", DisplayName: "HandleHTTPRequest", Kind: "function", DocumentID: docID, Definition: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
			{ID: callee, UnitID: unit, StableName: "example.authorize", DisplayName: "authorize", Kind: "function", DocumentID: docID, Definition: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
		},
		Occurrences: []graph.Occurrence{{ID: unit + ":occ:1", UnitID: unit, SymbolID: callee, DocumentID: docID, Role: "reference", Range: rng, Provider: "fixture", Evidence: graph.EvidenceExact}},
		Edges:       []graph.Edge{{ID: unit + ":edge:1", UnitID: unit, From: caller, To: callee, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, DocumentID: docID, Range: rng, Provider: "fixture"}},
	}
}

func TestQueryUsesIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	if err := db.ReplaceUnit(ctx, fixtureFacts("unit-a")); err != nil {
		t.Fatal(err)
	}
	before := db.db.Stats()
	if _, _, err := db.EdgesFrom(ctx, "unit-a:HandleHTTPRequest", nil, 10); err != nil {
		t.Fatal(err)
	}
	delta := db.db.Stats().Sub(before)
	if delta.PlanIndexScan == 0 || delta.PlanTableScan != 0 {
		t.Fatalf("adjacency query plan = %+v, want index scan and no table scan", delta)
	}
}

func TestDatabaseGenerationLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	defer db.Close()
	if got, err := db.Generation(ctx); err != nil || got != "" {
		t.Fatalf("initial Generation = %q, %v", got, err)
	}
	if err := db.SetGeneration(ctx, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Generation(ctx); err != nil || got != "sha256:first" {
		t.Fatalf("set Generation = %q, %v", got, err)
	}
	if err := db.InvalidateGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Generation(ctx); err != nil || got != "" {
		t.Fatalf("invalidated Generation = %q, %v", got, err)
	}
	if err := db.SetGeneration(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty SetGeneration error = %v", err)
	}
}

var _ = bstore.ErrAbsent // Keep fixture tests explicit about the storage dependency.
