package query_test

import (
	"context"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
)

func TestPathChoosesDeterministicShortestRoute(t *testing.T) {
	t.Parallel()
	store := fakeStore{forward: map[string][]graph.Edge{
		"a": {{ID: "ab", From: "a", To: "b", Kind: graph.EdgeCalls}, {ID: "ac", From: "a", To: "c", Kind: graph.EdgeCalls}},
		"b": {{ID: "bd", From: "b", To: "d", Kind: graph.EdgeCalls}},
		"c": {{ID: "cd", From: "c", To: "d", Kind: graph.EdgeCalls}},
	}}
	got, err := query.Path(context.Background(), store, "a", "d", nil, query.Bounds{MaxDepth: 4, MaxNodes: 10, MaxEdges: 20})
	if err != nil {
		t.Fatal(err)
	}
	// The store contract supplies canonical adjacency, so b wins the tie.
	if len(got.Edges) != 2 || got.Edges[0].ID != "ab" || got.Edges[1].ID != "bd" {
		t.Fatalf("Path() = %#v", got)
	}
}

func TestImpactReportsNodeBoundTruncation(t *testing.T) {
	t.Parallel()
	store := fakeStore{reverse: map[string][]graph.Edge{
		"root": {{ID: "a", From: "a", To: "root"}, {ID: "b", From: "b", To: "root"}},
	}}
	got, err := query.Impact(context.Background(), store, "root", nil, query.Bounds{MaxDepth: 4, MaxNodes: 2, MaxEdges: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Nodes) != 2 {
		t.Fatalf("Impact() = %#v, want two nodes and truncation", got)
	}
}

func TestImpactManySortsAndSharesTraversalBounds(t *testing.T) {
	t.Parallel()
	store := fakeStore{reverse: map[string][]graph.Edge{
		"a": {{ID: "ca", From: "caller", To: "a"}},
		"b": {{ID: "cb", From: "caller", To: "b"}},
	}}
	got, err := query.ImpactMany(context.Background(), store, []string{"b", "a", "b"}, nil, query.Bounds{MaxDepth: 4, MaxNodes: 10, MaxEdges: 20})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Nodes, ",") != "a,b,caller" || len(got.Edges) != 1 || got.Edges[0].ID != "ca" {
		t.Fatalf("ImpactMany() = %#v", got)
	}
}

func TestNeighborhoodInterleavesIncomingAndOutgoingWalks(t *testing.T) {
	t.Parallel()
	store := fakeStore{
		forward: map[string][]graph.Edge{
			"root":       {{ID: "root-dependency", From: "root", To: "dependency", Kind: graph.EdgeDependsOn}},
			"dependency": {{ID: "dependency-leaf", From: "dependency", To: "leaf", Kind: graph.EdgeDependsOn}},
		},
		reverse: map[string][]graph.Edge{
			"root":     {{ID: "importer-root", From: "importer", To: "root", Kind: graph.EdgeDependsOn}},
			"importer": {{ID: "entry-importer", From: "entry", To: "importer", Kind: graph.EdgeDependsOn}},
		},
	}
	got, err := query.Neighborhood(context.Background(), store, "root", []graph.EdgeKind{graph.EdgeDependsOn}, query.DirectionBoth, query.Bounds{MaxDepth: 2, MaxNodes: 10, MaxEdges: 20})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Nodes, ",") != "root,dependency,importer,leaf,entry" || len(got.Edges) != 4 {
		t.Fatalf("Neighborhood() = %#v", got)
	}
}

func TestNeighborhoodSharesBoundsAndRejectsInvalidDirection(t *testing.T) {
	t.Parallel()
	store := fakeStore{forward: map[string][]graph.Edge{
		"root": {
			{ID: "root-a", From: "root", To: "a"},
			{ID: "root-b", From: "root", To: "b"},
		},
	}}
	got, err := query.Neighborhood(context.Background(), store, "root", nil, query.DirectionOutgoing, query.Bounds{MaxDepth: 2, MaxNodes: 2, MaxEdges: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || strings.Join(got.Nodes, ",") != "root,a" || len(got.Edges) != 1 {
		t.Fatalf("bounded Neighborhood() = %#v", got)
	}
	if _, err := query.Neighborhood(context.Background(), store, "root", nil, "sideways", query.Bounds{MaxDepth: 2, MaxNodes: 2, MaxEdges: 20}); err == nil {
		t.Fatal("invalid direction succeeded")
	}
}

type fakeStore struct{ forward, reverse map[string][]graph.Edge }

func (fakeStore) FindSymbols(context.Context, string, int) ([]graph.Symbol, bool, error) {
	return nil, false, nil
}
func (fakeStore) Symbol(context.Context, string) (graph.Symbol, bool, error) {
	return graph.Symbol{}, false, nil
}
func (s fakeStore) EdgesFrom(_ context.Context, id string, _ []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return bounded(s.forward[id], limit)
}
func (s fakeStore) EdgesTo(_ context.Context, id string, _ []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return bounded(s.reverse[id], limit)
}
func bounded(edges []graph.Edge, limit int) ([]graph.Edge, bool, error) {
	if len(edges) > limit {
		return edges[:limit], true, nil
	}
	return edges, false, nil
}
