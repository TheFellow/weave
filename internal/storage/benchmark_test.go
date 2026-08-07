package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
)

func BenchmarkIndexedGraph(b *testing.B) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "benchmark.db"), Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })

	facts := graph.UnitFacts{
		Unit:      graph.Unit{ID: "benchmark", Provider: "benchmark", ProviderVersion: "1"},
		Documents: []graph.Document{{ID: "benchmark:document", UnitID: "benchmark", Path: "benchmark.go", Language: "go", Provider: "benchmark", ProviderVersion: "1"}},
	}
	for i := range 2_000 {
		id := fmt.Sprintf("benchmark:symbol:%04d", i)
		facts.Symbols = append(facts.Symbols, graph.Symbol{ID: id, UnitID: "benchmark", StableName: id, DisplayName: fmt.Sprintf("Handler%04d", i), Kind: "function", DocumentID: "benchmark:document", Provider: "benchmark", Evidence: graph.EvidenceExact})
		if i > 0 {
			facts.Edges = append(facts.Edges, graph.Edge{ID: fmt.Sprintf("benchmark:edge:%04d", i), UnitID: "benchmark", From: id, To: fmt.Sprintf("benchmark:symbol:%04d", i-1), Kind: graph.EdgeCalls, Provider: "benchmark", Evidence: graph.EvidenceExact})
		}
	}
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		b.Fatal(err)
	}

	b.Run("FindSymbolsPrefix50", func(b *testing.B) {
		for b.Loop() {
			if _, _, err := db.FindSymbols(ctx, "Handler1", 50); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("EdgesFrom", func(b *testing.B) {
		for b.Loop() {
			if _, _, err := db.EdgesFrom(ctx, "benchmark:symbol:1000", []graph.EdgeKind{graph.EdgeCalls}, 50); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Export", func(b *testing.B) {
		for b.Loop() {
			if _, err := db.Export(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
}
