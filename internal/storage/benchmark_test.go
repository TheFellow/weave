package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/mjl-/bstore"
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

func BenchmarkStorageV1V2Representative(b *testing.B) {
	ctx := context.Background()
	facts := representativeFacts(5_000)
	focus := facts.Symbols[2_500].ID
	for _, version := range []string{"v1", "v2"} {
		b.Run(version, func(b *testing.B) {
			path := benchmarkDatabasePath(b, version)
			var legacy *bstore.DB
			var current *DB
			if version == "v1" {
				legacy = createLegacyV1(b, path, facts)
			} else {
				current = openTestDB(b, path)
				if err := current.ReplaceUnit(ctx, facts); err != nil {
					b.Fatal(err)
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(info.Size()), "db-bytes")
			b.Run("Size", func(b *testing.B) {
				for b.Loop() {
				}
				b.ReportMetric(float64(info.Size()), "db-bytes")
			})
			b.Run("Prefix50", func(b *testing.B) {
				for b.Loop() {
					if version == "v1" {
						if _, err := legacyFindSymbols(ctx, legacy, "HandlerWithLongRepeatedSuffix04", 50); err != nil {
							b.Fatal(err)
						}
					} else {
						if _, _, err := current.FindSymbols(ctx, "HandlerWithLongRepeatedSuffix04", 50); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("Adjacency", func(b *testing.B) {
				for b.Loop() {
					if version == "v1" {
						if _, err := legacyEdgesFrom(ctx, legacy, focus, 50); err != nil {
							b.Fatal(err)
						}
					} else {
						if _, _, err := current.EdgesFrom(ctx, focus, nil, 50); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("Export", func(b *testing.B) {
				for b.Loop() {
					if version == "v1" {
						if _, err := legacyExport(ctx, legacy); err != nil {
							b.Fatal(err)
						}
					} else {
						if _, err := current.Export(ctx); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("Replace", func(b *testing.B) {
				for b.Loop() {
					if version == "v1" {
						if err := replaceLegacyV1(ctx, legacy, facts); err != nil {
							b.Fatal(err)
						}
					} else {
						if err := current.ReplaceUnit(ctx, facts); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			if version == "v1" {
				if err := legacy.Close(); err != nil {
					b.Fatal(err)
				}
			} else {
				if err := current.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.Run("Open", func(b *testing.B) {
				for b.Loop() {
					if version == "v1" {
						db, err := bstore.Open(ctx, path, &bstore.Options{MustExist: true}, legacyRecordTypes...)
						if err != nil {
							b.Fatal(err)
						}
						if err := db.Close(); err != nil {
							b.Fatal(err)
						}
					} else {
						db, err := Open(ctx, path, Options{MustExist: true})
						if err != nil {
							b.Fatal(err)
						}
						if err := db.Close(); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			source, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("Compact", func(b *testing.B) {
				for b.Loop() {
					copyPath := filepath.Join(b.TempDir(), "compact.db")
					if err := os.WriteFile(copyPath, source, 0o600); err != nil {
						b.Fatal(err)
					}
					if err := Compact(ctx, copyPath); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func TestRepresentativeV2SizeDoesNotRegressV1(t *testing.T) {
	if testing.Short() {
		t.Skip("representative storage size comparison")
	}
	facts := representativeFacts(2_000)
	v1Path := filepath.Join(t.TempDir(), "v1.db")
	v1 := createLegacyV1(t, v1Path, facts)
	if err := v1.Close(); err != nil {
		t.Fatal(err)
	}
	v2Path := filepath.Join(t.TempDir(), "v2.db")
	v2 := openTestDB(t, v2Path)
	if err := v2.ReplaceUnit(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	if err := v2.Close(); err != nil {
		t.Fatal(err)
	}
	v1Info, err := os.Stat(v1Path)
	if err != nil {
		t.Fatal(err)
	}
	v2Info, err := os.Stat(v2Path)
	if err != nil {
		t.Fatal(err)
	}
	if v2Info.Size() >= v1Info.Size() {
		t.Fatalf("v2 database = %d bytes, v1 = %d", v2Info.Size(), v1Info.Size())
	}
}
