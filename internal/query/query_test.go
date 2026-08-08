package query_test

import (
	"context"
	"errors"
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

func TestResolveUniqueAcceptsExactIDsAndRejectsAmbiguousQueries(t *testing.T) {
	t.Parallel()
	store := fakeStore{
		symbols: map[string]graph.Symbol{"exact": {ID: "exact", DisplayName: "Guide"}},
		matches: []graph.Symbol{{ID: "guide-one", DisplayName: "Guide"}, {ID: "guide-two", DisplayName: "Guide"}},
	}
	resolved, err := query.ResolveUnique(context.Background(), store, "exact")
	if err != nil || resolved.ID != "exact" {
		t.Fatalf("exact ResolveUnique = %#v, %v", resolved, err)
	}
	if _, err := query.ResolveUnique(context.Background(), store, "Guide"); !errors.Is(err, query.ErrAmbiguous) || !strings.Contains(err.Error(), "guide-one") {
		t.Fatalf("ambiguous ResolveUnique error = %v", err)
	}
}

func TestResolveAcceptsHumanQualifiedCompilerNames(t *testing.T) {
	t.Parallel()
	symbol := graph.Symbol{
		ID:          "opaque-readiness-id",
		StableName:  "example.test/menus/internal/availability.type.AvailabilityCalculator.method.Readiness",
		DisplayName: "Readiness",
		Kind:        "method",
	}
	store := fakeStore{matchesByQuery: map[string][]graph.Symbol{
		"AvailabilityCalculator.Readiness": nil,
		"Readiness":                        {symbol},
	}}
	for _, resolve := range []func(context.Context, query.Store, string) (graph.Symbol, error){query.Resolve, query.ResolveUnique} {
		got, err := resolve(context.Background(), store, "AvailabilityCalculator.Readiness")
		if err != nil || got.ID != symbol.ID {
			t.Fatalf("qualified resolution = %#v, %v", got, err)
		}
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

type fakeStore struct {
	forward, reverse map[string][]graph.Edge
	symbols          map[string]graph.Symbol
	matches          []graph.Symbol
	matchesByQuery   map[string][]graph.Symbol
}

func (s fakeStore) FindSymbols(_ context.Context, value string, limit int) ([]graph.Symbol, bool, error) {
	matches := s.matches
	if s.matchesByQuery != nil {
		matches = s.matchesByQuery[value]
	}
	if len(matches) > limit {
		return matches[:limit], true, nil
	}
	return matches, false, nil
}
func (s fakeStore) Symbol(_ context.Context, id string) (graph.Symbol, bool, error) {
	symbol, ok := s.symbols[id]
	return symbol, ok, nil
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
