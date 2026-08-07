package graphdiff_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
)

func TestCompareSeparatesStableAddedRemovedChangedFacts(t *testing.T) {
	before := graph.Snapshot{Schema: graph.SchemaVersion,
		Units:       []graph.Unit{{ID: "changed", Provider: "go", SurfaceFingerprint: "old"}, {ID: "removed", Provider: "go"}},
		Documents:   []graph.Document{{ID: "doc", Path: "old.go"}},
		Symbols:     []graph.Symbol{{ID: "same", DisplayName: "Before"}, {ID: "removed"}},
		Occurrences: []graph.Occurrence{{ID: "occ", SymbolID: "same", Role: "definition"}},
		Edges:       []graph.Edge{{ID: "edge", From: "same", To: "removed", Kind: graph.EdgeCalls}},
	}
	after := graph.Snapshot{Schema: graph.SchemaVersion,
		Units:       []graph.Unit{{ID: "changed", Provider: "go", SurfaceFingerprint: "new"}, {ID: "added", Provider: "workspace"}},
		Documents:   []graph.Document{{ID: "doc", Path: "new.go"}},
		Symbols:     []graph.Symbol{{ID: "same", DisplayName: "After"}, {ID: "added"}},
		Occurrences: []graph.Occurrence{{ID: "occ", SymbolID: "same", Role: "reference"}},
		Edges:       []graph.Edge{{ID: "edge", From: "added", To: "same", Kind: graph.EdgeCalls}},
	}

	delta, err := graphdiff.Compare(context.Background(), before, after, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Units.Added) != 1 || delta.Units.Added[0].ID != "added" || len(delta.Units.Removed) != 1 || delta.Units.Removed[0].ID != "removed" || len(delta.Units.Changed) != 1 {
		t.Fatalf("unit delta = %#v", delta.Units)
	}
	if len(delta.Symbols.Added) != 1 || len(delta.Symbols.Removed) != 1 || delta.Symbols.Changed[0].Before.DisplayName != "Before" || delta.Symbols.Changed[0].After.DisplayName != "After" {
		t.Fatalf("symbol delta = %#v", delta.Symbols)
	}
	if len(delta.Documents.Changed) != 1 || len(delta.Occurrences.Changed) != 1 || len(delta.Edges.Changed) != 1 {
		t.Fatalf("fact delta = %#v", delta)
	}
	transitions := graphdiff.Transitions(delta)
	if len(transitions.Nodes) != 3 || transitions.Nodes[0] != (graphdiff.Transition{ID: "added", Status: "added"}) || transitions.Nodes[2] != (graphdiff.Transition{ID: "same", Status: "changed"}) || len(transitions.Edges) != 1 || transitions.Edges[0].ID != "edge" || transitions.Edges[0].Status != "changed" {
		t.Fatalf("transitions = %#v", transitions)
	}
	api := graphdiff.Surfaces(before, after, 10)
	if len(api.Surfaces) != 1 || api.Surfaces[0].Change != "changed" || api.Surfaces[0].Compatibility != "unknown" || api.Surfaces[0].Evidence != "provider-surface-fingerprint" {
		t.Fatalf("API delta = %#v", api)
	}
}

func TestCompareIsDeterministicAndIndependentlyBounded(t *testing.T) {
	after := graph.Snapshot{Schema: graph.SchemaVersion}
	for _, id := range []string{"z", "a", "m"} {
		after.Symbols = append(after.Symbols, graph.Symbol{ID: id})
		after.Edges = append(after.Edges, graph.Edge{ID: id, From: id, To: "target"})
	}
	first, err := graphdiff.Compare(context.Background(), graph.Snapshot{}, after, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := graphdiff.Compare(context.Background(), graph.Snapshot{}, after, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !first.Truncated {
		t.Fatalf("bounded deterministic delta = %#v / %#v", first, second)
	}
	if got := []string{first.Symbols.Added[0].ID, first.Symbols.Added[1].ID}; !reflect.DeepEqual(got, []string{"a", "m"}) {
		t.Fatalf("symbol ordering = %v", got)
	}
	encoded, err := json.Marshal(graphdiff.Result{Schema: graphdiff.Schema, Graph: &first})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 {
		t.Fatal("empty JSON")
	}
}

func TestCurrentRootsNeverSeedsRemovedOnlyFacts(t *testing.T) {
	current := graph.Snapshot{Symbols: []graph.Symbol{{ID: "current", UnitID: "unit", DocumentID: "doc"}, {ID: "unit-member", UnitID: "changed"}}}
	delta := graphdiff.Facts{
		Units:     graphdiff.Delta[graph.Unit]{Changed: []graphdiff.Change[graph.Unit]{{Before: graph.Unit{ID: "changed"}, After: graph.Unit{ID: "changed"}}}},
		Documents: graphdiff.Delta[graph.Document]{Changed: []graphdiff.Change[graph.Document]{{Before: graph.Document{ID: "doc"}, After: graph.Document{ID: "doc"}}}},
		Symbols: graphdiff.Delta[graph.Symbol]{
			Added: []graph.Symbol{{ID: "added"}}, Removed: []graph.Symbol{{ID: "removed"}},
		},
		Edges: graphdiff.Delta[graph.Edge]{Added: []graph.Edge{{ID: "edge", From: "added", To: "current"}}},
	}
	got := graphdiff.CurrentRoots(current, delta)
	want := []string{"added", "current", "unit-member"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
}

func TestCompareCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := graphdiff.Compare(ctx, graph.Snapshot{Units: []graph.Unit{{ID: "a"}}}, graph.Snapshot{}, 10); err == nil {
		t.Fatal("canceled comparison succeeded")
	}
}
