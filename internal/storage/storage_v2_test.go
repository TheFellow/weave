package storage

import (
	"bytes"
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

func TestLegacyV1RejectionDoesNotModifyDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy := createLegacyV1(t, path, fixtureFacts("legacy"))
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), path, Options{MustExist: true})
	if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "remove this disposable per-worktree index") || !strings.Contains(err.Error(), "weave index") {
		t.Fatalf("Open(v1) error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("schema rejection modified legacy database bytes")
	}
}

func TestV2LogicalExportMatchesV1AndRoundTripsRichFacts(t *testing.T) {
	t.Parallel()
	facts := fixtureFacts("unit-λ-" + strings.Repeat("long", 80))
	facts.Unit.Variant = "windows/arm64/日本語"
	facts.Unit.InputFingerprint = strings.Repeat("0123456789abcdef", 32)
	facts.Documents[0].Path = "資料/非常に長い/" + strings.Repeat("segment/", 40) + "main.go"
	facts.Symbols[0].DisplayName = "Handle世界" + strings.Repeat("X", 512)
	facts.Symbols[0].NormalizedName = graph.NormalizeName(facts.Symbols[0].DisplayName)
	facts.Symbols[0].Definition = graph.Range{Start: graph.Position{Line: 1234, Column: 56, Byte: 78901}, End: graph.Position{Line: 1235, Column: 7, Byte: 79000}}
	facts.Occurrences[0].Range = facts.Symbols[0].Definition
	facts.Edges[0].Range = facts.Symbols[0].Definition

	legacyPath := filepath.Join(t.TempDir(), "v1.db")
	legacy := createLegacyV1(t, legacyPath, facts)
	want, err := legacyExport(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	v2 := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer v2.Close()
	if err := v2.ReplaceUnit(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	got, err := v2.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("v2 export differs from v1\n got: %#v\nwant: %#v", got, want)
	}
}

func TestInternAndEntityLifecycleAcrossReplacementAndDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer db.Close()
	first, second := fixtureFacts("first"), fixtureFacts("second")
	// A cross-unit endpoint keeps the first symbol identity live when first is removed.
	second.Edges[0].To = first.Symbols[0].ID
	if err := db.ReplaceUnits(ctx, []graph.UnitFacts{first, second}, nil); err != nil {
		t.Fatal(err)
	}
	provider, err := bstore.QueryDB[internRecord](ctx, db.db).FilterEqual("Value", "fixture").Get()
	if err != nil {
		t.Fatal(err)
	}
	if provider.Refs != 12 {
		t.Fatalf("provider refs = %d, want 12", provider.Refs)
	}
	identity, err := bstore.QueryDB[entityRecord](ctx, db.db).FilterEqual("StableID", first.Symbols[0].ID).Get()
	if err != nil {
		t.Fatal(err)
	}
	if identity.Refs != 3 {
		t.Fatalf("shared identity refs = %d, want 3", identity.Refs)
	}
	if err := db.ReplaceUnits(ctx, nil, []string{"first"}); err != nil {
		t.Fatal(err)
	}
	identity, err = bstore.QueryDB[entityRecord](ctx, db.db).FilterEqual("StableID", first.Symbols[0].ID).Get()
	if err != nil {
		t.Fatal(err)
	}
	if identity.Refs != 1 {
		t.Fatalf("remaining external identity refs = %d, want 1", identity.Refs)
	}
	if err := db.ReplaceUnits(ctx, nil, []string{"second"}); err != nil {
		t.Fatal(err)
	}
	for name, count := range map[string]int{
		"intern": countRecords[internRecord](t, ctx, db.db), "entity": countRecords[entityRecord](t, ctx, db.db), "symbol detail": countRecords[symbolDetailRecord](t, ctx, db.db), "occurrence detail": countRecords[occurrenceDetailRecord](t, ctx, db.db), "edge detail": countRecords[edgeDetailRecord](t, ctx, db.db),
	} {
		if count != 0 {
			t.Errorf("%s records after unit deletion = %d", name, count)
		}
	}
	issues, err := db.Verify(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatalf("Verify after deletion = %#v, %v", issues, err)
	}
}

func TestV2BoundedOrderingAndTruncationMatchV1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	facts := fixtureFacts("order")
	focus := facts.Symbols[0].ID
	facts.Edges = nil
	for i, kind := range []graph.EdgeKind{graph.EdgeWrites, graph.EdgeCalls, graph.EdgeCalls, graph.EdgeReferences, graph.EdgeContains} {
		to := "external:" + string(rune('z'-i))
		facts.Edges = append(facts.Edges, graph.Edge{ID: "edge:" + string(rune('e'-i)), UnitID: facts.Unit.ID, From: focus, To: to, Kind: kind, Evidence: graph.EvidenceDeclared, Provider: facts.Unit.Provider})
	}
	legacy := createLegacyV1(t, filepath.Join(t.TempDir(), "v1.db"), facts)
	defer legacy.Close()
	want, err := legacyEdgesFrom(ctx, legacy, focus, 4)
	if err != nil {
		t.Fatal(err)
	}
	v2 := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer v2.Close()
	if err := v2.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	got, truncated, err := v2.EdgesFrom(ctx, focus, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("v2 adjacency did not report truncation")
	}
	if !reflect.DeepEqual(got, want[:3]) {
		t.Fatalf("bounded ordering differs\n got: %#v\nwant: %#v", got, want[:3])
	}
}

func TestBoundedSearchCandidateOrderingSurvivesNumericIDChurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var units []graph.UnitFacts
	for i, id := range []string{"symbol:z", "symbol:y", "symbol:x", "symbol:w", "symbol:v", "symbol:u", "symbol:b", "symbol:a"} {
		facts := fixtureFacts("search-unit-" + string(rune('a'+i)))
		facts.Symbols = facts.Symbols[:1]
		facts.Symbols[0].ID = id
		facts.Symbols[0].StableName = "shared.stable"
		facts.Symbols[0].DisplayName = "SharedToken"
		facts.Symbols[0].NormalizedName = "sharedtoken"
		facts.Occurrences = nil
		facts.Edges = nil
		units = append(units, facts)
	}
	legacy := createLegacyV1(t, filepath.Join(t.TempDir(), "v1.db"), units[0])
	defer legacy.Close()
	for _, facts := range units[1:] {
		if err := insertLegacyV1(ctx, legacy, facts); err != nil {
			t.Fatal(err)
		}
	}
	v2 := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer v2.Close()
	if err := v2.ReplaceUnits(ctx, units, nil); err != nil {
		t.Fatal(err)
	}
	// Replacing the lexically first symbol moves its private numeric identity to
	// the end of the auto sequence. Pre-limit candidate ordering must not change.
	if err := v2.ReplaceUnit(ctx, units[len(units)-1]); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"SharedToken", "shared.stable", "shared"} {
		want, err := legacyFindSymbols(ctx, legacy, query, 1)
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := v2.FindSymbols(ctx, query, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(want) != 1 || len(got) != 1 || want[0].ID != got[0].ID {
			t.Fatalf("FindSymbols(%q) IDs = %v, want %v", query, symbolIDs(got), symbolIDs(want))
		}
	}
}

func TestConcurrentReplacementAndSplitRecordQueriesAreConsistent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer db.Close()
	base := fixtureFacts("concurrent")
	if err := db.ReplaceUnit(ctx, base); err != nil {
		t.Fatal(err)
	}
	errorsCh := make(chan error, 2)
	start := make(chan struct{})
	go func() {
		<-start
		for i := range 40 {
			facts := fixtureFacts("concurrent")
			facts.Unit.Provider = "provider-" + string(rune('a'+i%2))
			facts.Documents[0].Provider = facts.Unit.Provider
			for j := range facts.Symbols {
				facts.Symbols[j].Provider = facts.Unit.Provider
			}
			for j := range facts.Occurrences {
				facts.Occurrences[j].Provider = facts.Unit.Provider
			}
			for j := range facts.Edges {
				facts.Edges[j].Provider = facts.Unit.Provider
			}
			if err := db.ReplaceUnit(ctx, facts); err != nil {
				errorsCh <- err
				return
			}
		}
		errorsCh <- nil
	}()
	go func() {
		<-start
		for range 80 {
			if _, _, err := db.FindSymbols(ctx, "Handle", 2); err != nil {
				errorsCh <- err
				return
			}
			if _, _, err := db.EdgesFrom(ctx, base.Symbols[0].ID, nil, 2); err != nil {
				errorsCh <- err
				return
			}
			if _, err := db.Export(ctx); err != nil {
				errorsCh <- err
				return
			}
		}
		errorsCh <- nil
	}()
	close(start)
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanceledReplacementDoesNotPublishPartialV2Facts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer db.Close()
	initial := fixtureFacts("cancel")
	if err := db.ReplaceUnit(ctx, initial); err != nil {
		t.Fatal(err)
	}
	want, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	next := fixtureFacts("cancel")
	next.Symbols = next.Symbols[:1]
	next.Occurrences = nil
	next.Edges = nil
	if err := db.ReplaceUnit(canceled, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplaceUnit(canceled) error = %v", err)
	}
	got, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("canceled replacement changed published facts")
	}
}

func symbolIDs(values []graph.Symbol) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.ID
	}
	return result
}

func TestScanHotFactsMatchesExportAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer db.Close()
	facts := representativeFacts(80)
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var symbols []graph.Symbol
	var edges []graph.Edge
	if err := db.ScanHotFacts(ctx, func(v graph.Symbol) error { symbols = append(symbols, v); return nil }, func(v graph.Edge) error { edges = append(edges, v); return nil }); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(edges, graph.CompareEdges)
	if !reflect.DeepEqual(symbols, snapshot.Symbols) || !reflect.DeepEqual(edges, snapshot.Edges) {
		t.Fatal("hot scan differs from exported facts")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.ScanHotFacts(canceled, func(graph.Symbol) error { return nil }, func(graph.Edge) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan error = %v", err)
	}
}

func TestVerifyDetectsMissingColdDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "v2.db"))
	defer db.Close()
	if err := db.ReplaceUnit(ctx, fixtureFacts("cold")); err != nil {
		t.Fatal(err)
	}
	edge, err := bstore.QueryDB[edgeRecord](ctx, db.db).Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bstore.QueryDB[edgeDetailRecord](ctx, db.db).FilterID(edge.ID).Delete(); err != nil {
		t.Fatal(err)
	}
	issues, err := db.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range issues {
		if issue.Kind == "missing-edge-detail" && issue.Fatal() {
			found = true
		}
	}
	if !found {
		t.Fatalf("Verify = %#v", issues)
	}
}

func countRecords[T any](t testing.TB, ctx context.Context, db *bstore.DB) int {
	t.Helper()
	n, err := bstore.QueryDB[T](ctx, db).Count()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func FuzzV2StableIdentityRoundTrip(f *testing.F) {
	f.Add("ascii-id", "Provider", "go")
	f.Add("識別子-λ", "プロバイダ", "rust")
	f.Fuzz(func(t *testing.T, id, provider, language string) {
		id = strings.Map(func(r rune) rune {
			if r == 0 {
				return -1
			}
			return r
		}, id)
		provider = strings.Map(func(r rune) rune {
			if r == 0 {
				return -1
			}
			return r
		}, provider)
		language = strings.Map(func(r rune) rune {
			if r == 0 {
				return -1
			}
			return r
		}, language)
		if id == "" || provider == "" || len(id) > 256 || len(provider) > 128 || len(language) > 64 {
			return
		}
		unit := "unit:" + id
		facts := fixtureFacts(unit)
		facts.Unit.Provider = provider
		facts.Unit.Language = language
		facts.Documents[0].Provider = provider
		facts.Documents[0].Language = language
		for i := range facts.Symbols {
			facts.Symbols[i].Provider = provider
		}
		for i := range facts.Occurrences {
			facts.Occurrences[i].Provider = provider
		}
		for i := range facts.Edges {
			facts.Edges[i].Provider = provider
		}
		db := openTestDB(t, filepath.Join(t.TempDir(), "fuzz.db"))
		defer db.Close()
		if err := db.ReplaceUnit(context.Background(), facts); err != nil {
			t.Fatal(err)
		}
		snapshot, err := db.Export(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Units) != 1 || snapshot.Units[0].ID != unit || snapshot.Units[0].Provider != provider || snapshot.Units[0].Language != language {
			t.Fatalf("round trip = %#v", snapshot.Units)
		}
	})
}
