package aggregate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/mjl-/bstore"
)

// Close releases the immutable generation handle.
func (db *DB) Close() error {
	if db == nil || db.db == nil {
		return nil
	}
	err := db.db.Close()
	db.db = nil
	return err
}

// Path returns the exact immutable generation database path.
func (db *DB) Path() string { return db.path }

// Generation returns the source-set generation proven when the DB was opened.
func (db *DB) Generation() string { return db.generation }

func (db *DB) Symbol(ctx context.Context, id string) (graph.Symbol, bool, error) {
	records, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterEqual("FactID", id).List()
	if err != nil {
		return graph.Symbol{}, false, fmt.Errorf("query aggregate symbol: %w", err)
	}
	if len(records) == 0 {
		return graph.Symbol{}, false, nil
	}
	canonical := records[0]
	for _, record := range records {
		if symbolRecordKey(record) < symbolRecordKey(canonical) {
			canonical = record
		}
		if err := db.recordSource("symbol", record.FactID, record.SourceID); err != nil {
			return graph.Symbol{}, false, err
		}
	}
	return fromSymbolRecord(canonical), true, nil
}

func (db *DB) FindSymbols(ctx context.Context, value string, limit int) ([]graph.Symbol, bool, error) {
	if limit < 1 || limit > 100_000 {
		return nil, false, fmt.Errorf("%w: limit must be between 1 and 100000", ErrInvalid)
	}
	normalized := graph.NormalizeName(value)
	if normalized == "" {
		return nil, false, fmt.Errorf("%w: symbol query is empty", ErrInvalid)
	}
	byID := map[string]graph.Symbol{}
	truncated := false
	if err := db.db.Read(ctx, func(tx *bstore.Tx) error {
		for _, sourceID := range db.sourceIDs {
			values, sourceTruncated, err := findSymbolsSource(tx, sourceID, value, limit)
			if err != nil {
				return err
			}
			truncated = truncated || sourceTruncated
			for _, symbol := range values {
				if err := db.recordSource("symbol", symbol.ID, sourceID); err != nil {
					return err
				}
				if current, exists := byID[symbol.ID]; !exists || symbolGraphKey(symbol) < symbolGraphKey(current) {
					byID[symbol.ID] = symbol
				}
			}
		}
		return nil
	}); err != nil {
		return nil, false, err
	}
	results := make([]graph.Symbol, 0, len(byID))
	for _, symbol := range byID {
		results = append(results, symbol)
	}
	slices.SortFunc(results, func(a, b graph.Symbol) int {
		rank := func(symbol graph.Symbol) int {
			switch {
			case symbol.ID == value || symbol.StableName == value:
				return 0
			case symbol.NormalizedName == normalized:
				return 1
			case strings.HasPrefix(symbol.NormalizedName, normalized):
				return 2
			default:
				return 3
			}
		}
		if left, right := rank(a), rank(b); left != right {
			return left - right
		}
		return strings.Compare(symbolGraphKey(a), symbolGraphKey(b))
	})
	truncated = truncated || len(results) > limit
	if len(results) > limit {
		results = results[:limit]
	}
	return results, truncated, nil
}

func findSymbolsSource(tx *bstore.Tx, sourceID, value string, limit int) ([]graph.Symbol, bool, error) {
	normalized := graph.NormalizeName(value)
	candidateLimit := limit*4 + 1
	byID := map[string]graph.Symbol{}
	exact, err := bstore.QueryTx[symbolRecord](tx).FilterEqual("SourceID", sourceID).FilterEqual("FactID", value).Get()
	if err == nil {
		byID[exact.FactID] = fromSymbolRecord(exact)
	} else if err != bstore.ErrAbsent {
		return nil, false, fmt.Errorf("search aggregate exact symbol: %w", err)
	}
	stable, err := bstore.QueryTx[symbolRecord](tx).FilterEqual("SourceID", sourceID).FilterEqual("StableName", value).Limit(limit + 1).List()
	if err != nil {
		return nil, false, fmt.Errorf("search aggregate stable names: %w", err)
	}
	for _, record := range stable {
		byID[record.FactID] = fromSymbolRecord(record)
	}
	names, err := bstore.QueryTx[symbolRecord](tx).FilterEqual("SourceID", sourceID).
		FilterGreaterEqual("NormalizedName", normalized).FilterLess("NormalizedName", prefixEnd(normalized)).
		SortAsc("NormalizedName", "ID").Limit(candidateLimit).List()
	if err != nil {
		return nil, false, fmt.Errorf("search aggregate names: %w", err)
	}
	for _, record := range names {
		byID[record.FactID] = fromSymbolRecord(record)
	}
	postings, err := bstore.QueryTx[tokenRecord](tx).FilterEqual("SourceID", sourceID).
		FilterGreaterEqual("Token", normalized).FilterLess("Token", prefixEnd(normalized)).
		SortAsc("Token", "SymbolID").Limit(candidateLimit).List()
	if err != nil {
		return nil, false, fmt.Errorf("search aggregate tokens: %w", err)
	}
	for _, posting := range postings {
		record, err := bstore.QueryTx[symbolRecord](tx).FilterID(posting.SymbolID).Get()
		if err != nil {
			return nil, false, fmt.Errorf("load aggregate token symbol: %w", err)
		}
		if _, exists := byID[record.FactID]; !exists {
			byID[record.FactID] = fromSymbolRecord(record)
		}
	}
	results := make([]graph.Symbol, 0, len(byID))
	for _, symbol := range byID {
		results = append(results, symbol)
	}
	slices.SortFunc(results, func(a, b graph.Symbol) int {
		rank := func(symbol graph.Symbol) int {
			switch {
			case symbol.ID == value || symbol.StableName == value:
				return 0
			case symbol.NormalizedName == normalized:
				return 1
			case strings.HasPrefix(symbol.NormalizedName, normalized):
				return 2
			default:
				return 3
			}
		}
		if left, right := rank(a), rank(b); left != right {
			return left - right
		}
		if a.NormalizedName != b.NormalizedName {
			return strings.Compare(a.NormalizedName, b.NormalizedName)
		}
		return strings.Compare(a.ID, b.ID)
	})
	truncated := len(stable) > limit || len(names) == candidateLimit || len(postings) == candidateLimit || len(results) > limit
	if len(results) > limit {
		results = results[:limit]
	}
	return results, truncated, nil
}

func (db *DB) EdgesFrom(ctx context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return db.edges(ctx, "From", id, kinds, limit)
}

func (db *DB) EdgesTo(ctx context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return db.edges(ctx, "To", id, kinds, limit)
}

func (db *DB) edges(ctx context.Context, direction, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	if limit < 1 || limit > 100_000 {
		return nil, false, fmt.Errorf("%w: limit must be between 1 and 100000", ErrInvalid)
	}
	bySemantic := map[string]graph.Edge{}
	truncated := false
	if err := db.db.Read(ctx, func(tx *bstore.Tx) error {
		groups, complete, err := edgeGroupsFast(tx, db.sourceIDs, direction, id, kinds, limit)
		if err != nil {
			return err
		}
		for _, sourceID := range db.sourceIDs {
			var records []edgeRecord
			var sourceTruncated bool
			if complete {
				records = groups[sourceID]
				sortEdgeRecords(records, direction)
				sourceTruncated = len(records) > limit
				if sourceTruncated {
					records = records[:limit]
				}
			} else {
				records, sourceTruncated, err = edgesSource(tx, sourceID, direction, id, kinds, limit)
				if err != nil {
					return err
				}
			}
			truncated = truncated || sourceTruncated
			for _, record := range records {
				edge := fromEdgeRecord(record)
				canonical, exists := bySemantic[record.SemanticID]
				if !exists {
					bySemantic[record.SemanticID] = edge
					canonical = edge
				}
				if err := db.recordSource("edge", canonical.ID, sourceID); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return nil, false, err
	}
	result := make([]graph.Edge, 0, len(bySemantic))
	for _, edge := range bySemantic {
		result = append(result, edge)
	}
	slices.SortFunc(result, graph.CompareEdges)
	if len(result) > limit {
		result = result[:limit]
		truncated = true
	}
	return result, truncated, nil
}

func edgeGroupsFast(tx *bstore.Tx, sourceIDs []string, direction, id string, kinds []graph.EdgeKind, limit int) (map[string][]edgeRecord, bool, error) {
	// Fetch all matching variants in one indexed scan when the result remains
	// bounded. If a pathological adjacency exceeds the cap, fall back to exact
	// per-source bounded queries without retaining the oversized candidate set.
	cap := len(sourceIDs) * (limit + 1)
	if cap <= 0 || cap > 1_000_000 {
		return nil, false, nil
	}
	query := bstore.QueryTx[edgeRecord](tx).FilterEqual(direction, id)
	if len(kinds) != 0 {
		values := make([]any, len(kinds))
		for i, kind := range kinds {
			values[i] = string(kind)
		}
		query.FilterEqual("Kind", values...)
	}
	records, err := query.Limit(cap + 1).List()
	if err != nil {
		return nil, false, fmt.Errorf("query aggregate adjacency: %w", err)
	}
	if len(records) > cap {
		return nil, false, nil
	}
	groups := make(map[string][]edgeRecord, len(sourceIDs))
	for _, record := range records {
		groups[record.SourceID] = append(groups[record.SourceID], record)
	}
	return groups, true, nil
}

func sortEdgeRecords(records []edgeRecord, direction string) {
	slices.SortFunc(records, func(a, b edgeRecord) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if direction == "To" {
			if a.From != b.From {
				return strings.Compare(a.From, b.From)
			}
		} else if a.To != b.To {
			return strings.Compare(a.To, b.To)
		}
		return strings.Compare(a.FactID, b.FactID)
	})
}

func edgesSource(tx *bstore.Tx, sourceID, direction, id string, kinds []graph.EdgeKind, limit int) ([]edgeRecord, bool, error) {
	query := bstore.QueryTx[edgeRecord](tx).FilterEqual("SourceID", sourceID).FilterEqual(direction, id)
	if len(kinds) != 0 {
		values := make([]any, len(kinds))
		for i, kind := range kinds {
			values[i] = string(kind)
		}
		query.FilterEqual("Kind", values...)
	}
	other := "To"
	if direction == "To" {
		other = "From"
	}
	records, err := query.SortAsc("Kind", other, "ID").Limit(limit + 1).List()
	if err != nil {
		return nil, false, fmt.Errorf("query aggregate adjacency: %w", err)
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	return records, truncated, nil
}

// Sources returns deterministic provenance for facts observed through this DB.
func (db *DB) Sources() []Provenance {
	db.mu.Lock()
	defer db.mu.Unlock()
	result := make([]Provenance, 0, len(db.observed))
	for _, source := range db.observed {
		result = append(result, source)
	}
	slices.SortFunc(result, func(a, b Provenance) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if a.FactID != b.FactID {
			return strings.Compare(a.FactID, b.FactID)
		}
		if a.Repository != b.Repository {
			return strings.Compare(a.Repository, b.Repository)
		}
		return strings.Compare(a.WorktreeID, b.WorktreeID)
	})
	return result
}

func symbolRecordKey(symbol symbolRecord) string {
	return symbol.NormalizedName + "\x00" + symbol.StableName + "\x00" + symbol.FactID + "\x00" + symbol.DocumentID
}

func symbolGraphKey(symbol graph.Symbol) string {
	return symbol.NormalizedName + "\x00" + symbol.StableName + "\x00" + symbol.ID + "\x00" + symbol.DocumentID
}

func (db *DB) recordSource(kind, factID, sourceID string) error {
	source, ok := db.sources[sourceID]
	if !ok {
		return fmt.Errorf("query aggregate source provenance: source %q is absent", sourceID)
	}
	provenance := Provenance{Kind: kind, FactID: factID, Repository: source.Repository, WorktreeID: source.WorktreeID, Root: source.Root}
	key := kind + "\x00" + factID + "\x00" + source.ID
	db.mu.Lock()
	db.observed[key] = provenance
	db.mu.Unlock()
	return nil
}
