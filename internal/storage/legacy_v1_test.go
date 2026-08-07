package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/mjl-/bstore"
)

// These records intentionally retain the shipped v1 layout. They are test-only:
// production rejects v1 and asks the user to rebuild, while benchmarks keep an
// honest baseline instead of comparing v2 only with itself.
type legacyUnitRecord struct {
	ID                 string `bstore:"typename WeaveUnit"`
	Provider           string
	ProviderVersion    string
	Language           string
	Variant            string
	InputFingerprint   string
	SurfaceFingerprint string
	InventoryDigest    string
}
type legacyDocumentRecord struct {
	ID                                                     string `bstore:"typename WeaveDocument"`
	UnitID                                                 string `bstore:"index,index UnitID+Path"`
	Path, Language, ContentHash, Provider, ProviderVersion string
}
type legacySymbolRecord struct {
	ID                 string `bstore:"typename WeaveSymbol"`
	UnitID             string `bstore:"index"`
	StableName         string `bstore:"index"`
	DisplayName        string
	NormalizedName     string `bstore:"index"`
	Kind               string `bstore:"index"`
	DocumentID         string `bstore:"index"`
	Definition         graph.Range
	Provider, Evidence string
}
type legacyOccurrenceRecord struct {
	ID                 string `bstore:"typename WeaveOccurrence"`
	UnitID             string `bstore:"index"`
	SymbolID           string `bstore:"index SymbolID+DocumentID"`
	DocumentID         string `bstore:"index"`
	Role               string `bstore:"index"`
	Range              graph.Range
	Provider, Evidence string
}
type legacyEdgeRecord struct {
	ID             string `bstore:"typename WeaveEdge"`
	UnitID         string `bstore:"index"`
	From           string `bstore:"index From+Kind+To FromKindTo"`
	To             string `bstore:"index To+Kind+From ToKindFrom"`
	Kind, Evidence string
	DocumentID     string `bstore:"index"`
	Range          graph.Range
	Provider       string
}
type legacyTokenRecord struct {
	ID       string `bstore:"typename WeaveSymbolToken"`
	UnitID   string `bstore:"index"`
	Token    string `bstore:"index Token+SymbolID"`
	SymbolID string `bstore:"index"`
}

var legacyRecordTypes = []any{metadataRecord{}, generationRecord{}, legacyUnitRecord{}, legacyDocumentRecord{}, legacySymbolRecord{}, legacyOccurrenceRecord{}, legacyEdgeRecord{}, legacyTokenRecord{}}

func createLegacyV1(t testing.TB, path string, facts graph.UnitFacts) *bstore.DB {
	t.Helper()
	facts.Symbols = append([]graph.Symbol(nil), facts.Symbols...)
	for i := range facts.Symbols {
		if facts.Symbols[i].NormalizedName == "" {
			facts.Symbols[i].NormalizedName = graph.NormalizeName(facts.Symbols[i].DisplayName)
		}
	}
	db, err := bstore.Open(context.Background(), path, nil, legacyRecordTypes...)
	if err != nil {
		t.Fatal(err)
	}
	metadata := metadataRecord{ID: 1, Version: 1}
	if err := db.Insert(context.Background(), &metadata); err != nil {
		t.Fatal(err)
	}
	if err := insertLegacyV1(context.Background(), db, facts); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertLegacyV1(ctx context.Context, db *bstore.DB, facts graph.UnitFacts) error {
	return db.Write(ctx, func(tx *bstore.Tx) error {
		return insertLegacyV1Tx(tx, facts)
	})
}

func insertLegacyV1Tx(tx *bstore.Tx, facts graph.UnitFacts) error {
	unit := legacyUnitRecord{facts.Unit.ID, facts.Unit.Provider, facts.Unit.ProviderVersion, facts.Unit.Language, facts.Unit.Variant, facts.Unit.InputFingerprint, facts.Unit.SurfaceFingerprint, facts.Unit.InventoryDigest}
	if err := tx.Insert(&unit); err != nil {
		return err
	}
	for _, value := range facts.Documents {
		record := legacyDocumentRecord{value.ID, value.UnitID, value.Path, value.Language, value.ContentHash, value.Provider, value.ProviderVersion}
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	for _, value := range facts.Symbols {
		record := legacySymbolRecord{value.ID, value.UnitID, value.StableName, value.DisplayName, value.NormalizedName, value.Kind, value.DocumentID, value.Definition, value.Provider, string(value.Evidence)}
		if err := tx.Insert(&record); err != nil {
			return err
		}
		for _, token := range graph.Tokens(value.DisplayName) {
			posting := legacyTokenRecord{token + "\x1f" + value.ID, value.UnitID, token, value.ID}
			if err := tx.Insert(&posting); err != nil {
				return err
			}
		}
	}
	for _, value := range facts.Occurrences {
		record := legacyOccurrenceRecord{value.ID, value.UnitID, value.SymbolID, value.DocumentID, value.Role, value.Range, value.Provider, string(value.Evidence)}
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	for _, value := range facts.Edges {
		record := legacyEdgeRecord{value.ID, value.UnitID, value.From, value.To, string(value.Kind), string(value.Evidence), value.DocumentID, value.Range, value.Provider}
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	return nil
}

func replaceLegacyV1(ctx context.Context, db *bstore.DB, facts graph.UnitFacts) error {
	return db.Write(ctx, func(tx *bstore.Tx) error {
		for _, remove := range []func() error{
			func() error {
				_, err := bstore.QueryTx[legacyTokenRecord](tx).FilterEqual("UnitID", facts.Unit.ID).Delete()
				return err
			},
			func() error {
				_, err := bstore.QueryTx[legacyEdgeRecord](tx).FilterEqual("UnitID", facts.Unit.ID).Delete()
				return err
			},
			func() error {
				_, err := bstore.QueryTx[legacyOccurrenceRecord](tx).FilterEqual("UnitID", facts.Unit.ID).Delete()
				return err
			},
			func() error {
				_, err := bstore.QueryTx[legacySymbolRecord](tx).FilterEqual("UnitID", facts.Unit.ID).Delete()
				return err
			},
			func() error {
				_, err := bstore.QueryTx[legacyDocumentRecord](tx).FilterEqual("UnitID", facts.Unit.ID).Delete()
				return err
			},
		} {
			if err := remove(); err != nil {
				return err
			}
		}
		if _, err := bstore.QueryTx[legacyUnitRecord](tx).FilterID(facts.Unit.ID).Delete(); err != nil {
			return err
		}
		return insertLegacyV1Tx(tx, facts)
	})
}

func legacyExport(ctx context.Context, db *bstore.DB) (graph.Snapshot, error) {
	units, err := bstore.QueryDB[legacyUnitRecord](ctx, db).List()
	if err != nil {
		return graph.Snapshot{}, err
	}
	documents, err := bstore.QueryDB[legacyDocumentRecord](ctx, db).List()
	if err != nil {
		return graph.Snapshot{}, err
	}
	symbols, err := bstore.QueryDB[legacySymbolRecord](ctx, db).List()
	if err != nil {
		return graph.Snapshot{}, err
	}
	occurrences, err := bstore.QueryDB[legacyOccurrenceRecord](ctx, db).List()
	if err != nil {
		return graph.Snapshot{}, err
	}
	edges, err := bstore.QueryDB[legacyEdgeRecord](ctx, db).List()
	if err != nil {
		return graph.Snapshot{}, err
	}
	result := graph.Snapshot{Schema: graph.SchemaVersion}
	for _, v := range units {
		result.Units = append(result.Units, graph.Unit{ID: v.ID, Provider: v.Provider, ProviderVersion: v.ProviderVersion, Language: v.Language, Variant: v.Variant, InputFingerprint: v.InputFingerprint, SurfaceFingerprint: v.SurfaceFingerprint, InventoryDigest: v.InventoryDigest})
	}
	for _, v := range documents {
		result.Documents = append(result.Documents, graph.Document{ID: v.ID, UnitID: v.UnitID, Path: v.Path, Language: v.Language, ContentHash: v.ContentHash, Provider: v.Provider, ProviderVersion: v.ProviderVersion})
	}
	for _, v := range symbols {
		result.Symbols = append(result.Symbols, graph.Symbol{ID: v.ID, UnitID: v.UnitID, StableName: v.StableName, DisplayName: v.DisplayName, NormalizedName: v.NormalizedName, Kind: v.Kind, DocumentID: v.DocumentID, Definition: v.Definition, Provider: v.Provider, Evidence: graph.Evidence(v.Evidence)})
	}
	for _, v := range occurrences {
		result.Occurrences = append(result.Occurrences, graph.Occurrence{ID: v.ID, UnitID: v.UnitID, SymbolID: v.SymbolID, DocumentID: v.DocumentID, Role: v.Role, Range: v.Range, Provider: v.Provider, Evidence: graph.Evidence(v.Evidence)})
	}
	for _, v := range edges {
		result.Edges = append(result.Edges, graph.Edge{ID: v.ID, UnitID: v.UnitID, From: v.From, To: v.To, Kind: graph.EdgeKind(v.Kind), Evidence: graph.Evidence(v.Evidence), DocumentID: v.DocumentID, Range: v.Range, Provider: v.Provider})
	}
	graph.SortSnapshot(&result)
	return result, nil
}

func representativeFacts(count int) graph.UnitFacts {
	unit := "benchmark-unit"
	facts := graph.UnitFacts{Unit: graph.Unit{ID: unit, Provider: "benchmark-provider", ProviderVersion: "2026.08.07", Language: "go", Variant: "linux/amd64", InputFingerprint: strings.Repeat("i", 64), SurfaceFingerprint: strings.Repeat("s", 64), InventoryDigest: strings.Repeat("d", 64)}}
	for i := range max(1, count/25) {
		id := fmt.Sprintf("document:pkg/%04d/generated_component_with_a_long_name.go", i)
		facts.Documents = append(facts.Documents, graph.Document{ID: id, UnitID: unit, Path: strings.TrimPrefix(id, "document:"), Language: "go", ContentHash: fmt.Sprintf("sha256:%064x", i), Provider: facts.Unit.Provider, ProviderVersion: facts.Unit.ProviderVersion})
	}
	rng := graph.Range{Start: graph.Position{Line: 42, Column: 8, Byte: 4096}, End: graph.Position{Line: 42, Column: 32, Byte: 4120}}
	for i := range count {
		document := facts.Documents[i%len(facts.Documents)].ID
		id := fmt.Sprintf("symbol:scip go github.com/TheFellow/fixture v1 pkg/%04d/HandlerWithLongRepeatedSuffix#", i)
		facts.Symbols = append(facts.Symbols, graph.Symbol{ID: id, UnitID: unit, StableName: "github.com/TheFellow/fixture/pkg.HandlerWithLongRepeatedSuffix" + fmt.Sprint(i), DisplayName: fmt.Sprintf("HandlerWithLongRepeatedSuffix%05d", i), NormalizedName: fmt.Sprintf("handlerwithlongrepeatedsuffix%05d", i), Kind: "function", DocumentID: document, Definition: rng, Provider: facts.Unit.Provider, Evidence: graph.EvidenceExact})
		facts.Occurrences = append(facts.Occurrences, graph.Occurrence{ID: fmt.Sprintf("occurrence:%05d:reference", i), UnitID: unit, SymbolID: id, DocumentID: document, Role: "definition", Range: rng, Provider: facts.Unit.Provider, Evidence: graph.EvidenceExact})
		if i > 0 {
			facts.Edges = append(facts.Edges, graph.Edge{ID: fmt.Sprintf("edge:%05d:calls", i), UnitID: unit, From: id, To: facts.Symbols[i-1].ID, Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, DocumentID: document, Range: rng, Provider: facts.Unit.Provider})
		}
	}
	return facts
}

func legacyEdgesFrom(ctx context.Context, db *bstore.DB, symbol string, limit int) ([]graph.Edge, error) {
	records, err := bstore.QueryDB[legacyEdgeRecord](ctx, db).FilterEqual("From", symbol).SortAsc("Kind", "To", "ID").Limit(limit).List()
	if err != nil {
		return nil, err
	}
	result := make([]graph.Edge, len(records))
	for i, v := range records {
		result[i] = graph.Edge{ID: v.ID, UnitID: v.UnitID, From: v.From, To: v.To, Kind: graph.EdgeKind(v.Kind), Evidence: graph.Evidence(v.Evidence), DocumentID: v.DocumentID, Range: v.Range, Provider: v.Provider}
	}
	return result, nil
}

func legacyFindSymbols(ctx context.Context, db *bstore.DB, value string, limit int) ([]graph.Symbol, error) {
	normalized := graph.NormalizeName(value)
	candidateLimit := limit*4 + 1
	byID := map[string]graph.Symbol{}
	convert := func(v legacySymbolRecord) graph.Symbol {
		return graph.Symbol{ID: v.ID, UnitID: v.UnitID, StableName: v.StableName, DisplayName: v.DisplayName, NormalizedName: v.NormalizedName, Kind: v.Kind, DocumentID: v.DocumentID, Definition: v.Definition, Provider: v.Provider, Evidence: graph.Evidence(v.Evidence)}
	}
	if record, err := bstore.QueryDB[legacySymbolRecord](ctx, db).FilterID(value).Get(); err == nil {
		byID[record.ID] = convert(record)
	} else if err != bstore.ErrAbsent {
		return nil, err
	}
	stable, err := bstore.QueryDB[legacySymbolRecord](ctx, db).FilterEqual("StableName", value).Limit(limit + 1).List()
	if err != nil {
		return nil, err
	}
	for _, record := range stable {
		byID[record.ID] = convert(record)
	}
	names, err := bstore.QueryDB[legacySymbolRecord](ctx, db).FilterGreaterEqual("NormalizedName", normalized).FilterLess("NormalizedName", prefixEnd(normalized)).SortAsc("NormalizedName", "ID").Limit(candidateLimit).List()
	if err != nil {
		return nil, err
	}
	for _, record := range names {
		byID[record.ID] = convert(record)
	}
	postings, err := bstore.QueryDB[legacyTokenRecord](ctx, db).FilterGreaterEqual("Token", normalized).FilterLess("Token", prefixEnd(normalized)).SortAsc("Token", "SymbolID").Limit(candidateLimit).List()
	if err != nil {
		return nil, err
	}
	for _, posting := range postings {
		if _, ok := byID[posting.SymbolID]; ok {
			continue
		}
		record, err := bstore.QueryDB[legacySymbolRecord](ctx, db).FilterID(posting.SymbolID).Get()
		if err != nil {
			return nil, err
		}
		byID[record.ID] = convert(record)
	}
	result := make([]graph.Symbol, 0, len(byID))
	for _, symbol := range byID {
		result = append(result, symbol)
	}
	slices.SortFunc(result, func(a, b graph.Symbol) int {
		rank := func(v graph.Symbol) int {
			switch {
			case v.ID == value || v.StableName == value:
				return 0
			case v.NormalizedName == normalized:
				return 1
			case strings.HasPrefix(v.NormalizedName, normalized):
				return 2
			default:
				return 3
			}
		}
		if ar, br := rank(a), rank(b); ar != br {
			return ar - br
		}
		if a.NormalizedName != b.NormalizedName {
			return strings.Compare(a.NormalizedName, b.NormalizedName)
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func benchmarkDatabasePath(b *testing.B, name string) string {
	return filepath.Join(b.TempDir(), name+".db")
}
