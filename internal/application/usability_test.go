package application

import (
	"context"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
)

type displayFixtureStore struct {
	symbols   map[string]graph.Symbol
	documents map[string]graph.Document
	edges     []graph.Edge
}

func (store displayFixtureStore) FindSymbols(_ context.Context, value string, _ int) ([]graph.Symbol, bool, error) {
	if symbol, ok := store.symbols[value]; ok {
		return []graph.Symbol{symbol}, false, nil
	}
	return nil, false, nil
}

func (store displayFixtureStore) Symbol(_ context.Context, id string) (graph.Symbol, bool, error) {
	symbol, ok := store.symbols[id]
	return symbol, ok, nil
}

func (store displayFixtureStore) Document(_ context.Context, id string) (graph.Document, bool, error) {
	document, ok := store.documents[id]
	return document, ok, nil
}

func (store displayFixtureStore) EdgesFrom(_ context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	wanted := map[graph.EdgeKind]bool{}
	for _, kind := range kinds {
		wanted[kind] = true
	}
	var result []graph.Edge
	for _, edge := range store.edges {
		if edge.From == id && wanted[edge.Kind] {
			result = append(result, edge)
		}
	}
	truncated := len(result) > limit
	if truncated {
		result = result[:limit]
	}
	return result, truncated, nil
}

func (store displayFixtureStore) EdgesTo(_ context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	wanted := map[graph.EdgeKind]bool{}
	for _, kind := range kinds {
		wanted[kind] = true
	}
	var result []graph.Edge
	for _, edge := range store.edges {
		if edge.To == id && wanted[edge.Kind] {
			result = append(result, edge)
		}
	}
	truncated := len(result) > limit
	if truncated {
		result = result[:limit]
	}
	return result, truncated, nil
}

func TestDirectDependenciesCollapseRepeatedImportEvidence(t *testing.T) {
	store := displayFixtureStore{edges: []graph.Edge{
		{ID: "import-a", From: "source", To: "target", Kind: graph.EdgeImports, DocumentID: "a"},
		{ID: "depends-a", From: "source", To: "target", Kind: graph.EdgeDependsOn, DocumentID: "a"},
		{ID: "depends-b", From: "source", To: "target", Kind: graph.EdgeDependsOn, DocumentID: "b"},
		{ID: "depends-other", From: "source", To: "other", Kind: graph.EdgeDependsOn, DocumentID: "c"},
	}}
	edges, truncated, err := directDependencies(context.Background(), store, "source", 10)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(edges) != 2 || edges[0].Kind != graph.EdgeDependsOn || edges[1].Kind != graph.EdgeDependsOn {
		t.Fatalf("dependencies = %#v, truncated=%t", edges, truncated)
	}
}

func TestEnrichResponseHydratesNamesAndSourcePaths(t *testing.T) {
	store := displayFixtureStore{
		symbols: map[string]graph.Symbol{
			"caller": {ID: "caller", StableName: "example.Caller", DocumentID: "doc"},
			"callee": {ID: "callee", StableName: "example.Callee", DocumentID: "doc"},
		},
		documents: map[string]graph.Document{"doc": {ID: "doc", Path: "service.go"}},
	}
	response := Response{Edges: []graph.Edge{{From: "caller", To: "callee", Kind: graph.EdgeCalls, DocumentID: "doc"}}}
	if err := enrichResponse(context.Background(), store, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Symbols) != 2 || len(response.Documents) != 1 || response.Documents[0].Path != "service.go" {
		t.Fatalf("enriched response = %#v", response)
	}
}
