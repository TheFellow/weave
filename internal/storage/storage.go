// Package storage persists and queries Weave's normalized graph facts.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/mjl-/bstore"
	bolt "go.etcd.io/bbolt"
)

var (
	// ErrSchema indicates that a database uses an unsupported Weave schema.
	ErrSchema = errors.New("unsupported weave database schema")
	// ErrCorrupt indicates invalid physical storage or normalized records.
	ErrCorrupt = errors.New("corrupt weave database")
	// ErrInvalid indicates malformed facts or query bounds.
	ErrInvalid = errors.New("invalid graph operation")
)

const openTimeout = 2 * time.Second

// Options control database creation.
type Options struct {
	MustExist bool
	Timeout   time.Duration
}

// DB is a concurrency-safe handle to one Weave graph database.
type DB struct {
	path string
	db   *bstore.DB
}

type metadataRecord struct {
	ID      uint8 `bstore:"typename WeaveMetadata,noauto"`
	Version uint32
}

type unitRecord struct {
	ID                 string `bstore:"typename WeaveUnit"`
	Provider           string
	ProviderVersion    string
	Language           string
	Variant            string
	InputFingerprint   string
	SurfaceFingerprint string
	InventoryDigest    string
}

type documentRecord struct {
	ID              string `bstore:"typename WeaveDocument"`
	UnitID          string `bstore:"index,index UnitID+Path"`
	Path            string
	Language        string
	ContentHash     string
	Provider        string
	ProviderVersion string
}

type symbolRecord struct {
	ID             string `bstore:"typename WeaveSymbol"`
	UnitID         string `bstore:"index"`
	StableName     string `bstore:"index"`
	DisplayName    string
	NormalizedName string `bstore:"index"`
	Kind           string `bstore:"index"`
	DocumentID     string `bstore:"index"`
	Definition     graph.Range
	Provider       string
	Evidence       string
}

type occurrenceRecord struct {
	ID         string `bstore:"typename WeaveOccurrence"`
	UnitID     string `bstore:"index"`
	SymbolID   string `bstore:"index SymbolID+DocumentID"`
	DocumentID string `bstore:"index"`
	Role       string `bstore:"index"`
	Range      graph.Range
	Provider   string
	Evidence   string
}

type edgeRecord struct {
	ID         string `bstore:"typename WeaveEdge"`
	UnitID     string `bstore:"index"`
	From       string `bstore:"index From+Kind+To FromKindTo"`
	To         string `bstore:"index To+Kind+From ToKindFrom"`
	Kind       string
	Evidence   string
	DocumentID string `bstore:"index"`
	Range      graph.Range
	Provider   string
}

type tokenRecord struct {
	ID       string `bstore:"typename WeaveSymbolToken"`
	UnitID   string `bstore:"index"`
	Token    string `bstore:"index Token+SymbolID"`
	SymbolID string `bstore:"index"`
}

var recordTypes = []any{
	metadataRecord{}, unitRecord{}, documentRecord{}, symbolRecord{},
	occurrenceRecord{}, edgeRecord{}, tokenRecord{},
}

// Open opens or creates a database and validates its Weave schema marker.
func Open(ctx context.Context, path string, options Options) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: database path is empty", ErrInvalid)
	}
	if !options.MustExist {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = openTimeout
	}
	db, err := bstore.Open(ctx, path, &bstore.Options{MustExist: options.MustExist, Timeout: timeout}, recordTypes...)
	if err != nil {
		return nil, classify("open database", err)
	}
	store := &DB{path: path, db: db}
	if err := store.ensureMetadata(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (db *DB) ensureMetadata(ctx context.Context) error {
	metadata, err := bstore.QueryDB[metadataRecord](ctx, db.db).FilterID(uint8(1)).Get()
	if err == bstore.ErrAbsent {
		if err := db.db.Insert(ctx, &metadataRecord{ID: 1, Version: graph.SchemaVersion}); err != nil {
			return classify("initialize schema", err)
		}
		return nil
	}
	if err != nil {
		return classify("read schema", err)
	}
	if metadata.Version != graph.SchemaVersion {
		return fmt.Errorf("%w: database has version %d, executable supports %d; rebuild the derived index", ErrSchema, metadata.Version, graph.SchemaVersion)
	}
	return nil
}

// Close releases the database file lock.
func (db *DB) Close() error {
	if db == nil || db.db == nil {
		return nil
	}
	err := db.db.Close()
	db.db = nil
	return err
}

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

// ReplaceUnit atomically replaces every fact owned by one compilation unit.
func (db *DB) ReplaceUnit(ctx context.Context, facts graph.UnitFacts) error {
	return db.ReplaceUnits(ctx, []graph.UnitFacts{facts}, nil)
}

// ReplaceUnits atomically replaces complete returned compilation-unit batches
// and removes units no longer present in the provider's complete inventory.
func (db *DB) ReplaceUnits(ctx context.Context, batches []graph.UnitFacts, removed []string) error {
	if err := prepareReplacement(batches, removed); err != nil {
		return err
	}
	return db.replacePrepared(ctx, batches, removed)
}

// ReplaceUnitsIncremental applies bounded atomic chunks after validating the
// complete replacement set. A crash can leave an incomplete database, but the
// freshness manifest is published only after this method succeeds, so the next
// observation deterministically reapplies the complete provider result. This
// avoids holding hundreds of thousands of indexed bstore records in one Bolt
// transaction while retaining atomic replacement for every semantic unit.
func (db *DB) ReplaceUnitsIncremental(ctx context.Context, batches []graph.UnitFacts, removed []string, maxFacts int) error {
	if err := prepareReplacement(batches, removed); err != nil {
		return err
	}
	if maxFacts <= 0 {
		return db.replacePrepared(ctx, batches, removed)
	}
	if len(removed) != 0 {
		if err := db.replacePrepared(ctx, nil, removed); err != nil {
			return err
		}
	}
	for start := 0; start < len(batches); {
		end, count := start, 0
		for end < len(batches) {
			next := factCount(batches[end])
			if end > start && count+next > maxFacts {
				break
			}
			count += next
			end++
		}
		if err := db.replacePrepared(ctx, batches[start:end], nil); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func prepareReplacement(batches []graph.UnitFacts, removed []string) error {
	seen := map[string]bool{}
	primary := map[string]string{}
	for _, id := range removed {
		if id == "" || seen[id] {
			return fmt.Errorf("%w: duplicate or empty removed unit %q", ErrInvalid, id)
		}
		seen[id] = true
	}
	for i := range batches {
		facts := &batches[i]
		if err := facts.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if seen[facts.Unit.ID] {
			return fmt.Errorf("%w: duplicate unit %q", ErrInvalid, facts.Unit.ID)
		}
		seen[facts.Unit.ID] = true
		for j := range facts.Symbols {
			facts.Symbols[j].NormalizedName = graph.NormalizeName(facts.Symbols[j].DisplayName)
		}
		groups := []struct {
			kind string
			ids  []string
		}{
			{"document", idsOf(facts.Documents, func(value graph.Document) string { return value.ID })},
			{"symbol", idsOf(facts.Symbols, func(value graph.Symbol) string { return value.ID })},
			{"occurrence", idsOf(facts.Occurrences, func(value graph.Occurrence) string { return value.ID })},
			{"edge", idsOf(facts.Edges, func(value graph.Edge) string { return value.ID })},
		}
		for _, group := range groups {
			for _, id := range group.ids {
				key := group.kind + "\x00" + id
				if owner, exists := primary[key]; exists {
					return fmt.Errorf("%w: duplicate %s %q in units %q and %q", ErrInvalid, group.kind, id, owner, facts.Unit.ID)
				}
				primary[key] = facts.Unit.ID
			}
		}
	}
	return nil
}

func idsOf[T any](values []T, id func(T) string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = id(value)
	}
	return result
}

func factCount(facts graph.UnitFacts) int {
	return 1 + len(facts.Documents) + len(facts.Symbols) + len(facts.Occurrences) + len(facts.Edges)
}

func (db *DB) replacePrepared(ctx context.Context, batches []graph.UnitFacts, removed []string) error {
	return classify("replace compilation units", db.db.Write(ctx, func(tx *bstore.Tx) error {
		for _, id := range removed {
			if err := deleteUnit(tx, id); err != nil {
				return err
			}
		}
		for _, facts := range batches {
			if err := deleteUnit(tx, facts.Unit.ID); err != nil {
				return err
			}
			if err := insertUnit(tx, facts); err != nil {
				return err
			}
		}
		return nil
	}))
}

func deleteUnit(tx *bstore.Tx, unitID string) error {
	for _, deleteOwned := range []func() error{
		func() error {
			_, err := bstore.QueryTx[tokenRecord](tx).FilterEqual("UnitID", unitID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[edgeRecord](tx).FilterEqual("UnitID", unitID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[occurrenceRecord](tx).FilterEqual("UnitID", unitID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[symbolRecord](tx).FilterEqual("UnitID", unitID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[documentRecord](tx).FilterEqual("UnitID", unitID).Delete()
			return err
		},
	} {
		if err := deleteOwned(); err != nil {
			return err
		}
	}
	_, err := bstore.QueryTx[unitRecord](tx).FilterID(unitID).Delete()
	return err
}

func insertUnit(tx *bstore.Tx, facts graph.UnitFacts) error {
	unitID := facts.Unit.ID
	unit := toUnitRecord(facts.Unit)
	if err := tx.Insert(&unit); err != nil {
		return err
	}
	for _, document := range facts.Documents {
		record := toDocumentRecord(document)
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	for _, symbol := range facts.Symbols {
		record := toSymbolRecord(symbol)
		if err := tx.Insert(&record); err != nil {
			return err
		}
		for _, token := range graph.Tokens(symbol.DisplayName) {
			posting := tokenRecord{ID: tokenID(token, symbol.ID), UnitID: unitID, Token: token, SymbolID: symbol.ID}
			if err := tx.Insert(&posting); err != nil {
				return err
			}
		}
	}
	for _, occurrence := range facts.Occurrences {
		record := toOccurrenceRecord(occurrence)
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	for _, edge := range facts.Edges {
		record := toEdgeRecord(edge)
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	return nil
}

// Symbol returns a symbol by stable ID.
func (db *DB) Symbol(ctx context.Context, id string) (graph.Symbol, bool, error) {
	record, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterID(id).Get()
	if err == bstore.ErrAbsent {
		return graph.Symbol{}, false, nil
	}
	if err != nil {
		return graph.Symbol{}, false, classify("get symbol", err)
	}
	return fromSymbolRecord(record), true, nil
}

// Symbols returns all symbols in canonical order. This is intended for export
// and verification; user-facing searches should use FindSymbols.
func (db *DB) Symbols(ctx context.Context) ([]graph.Symbol, error) {
	records, err := bstore.QueryDB[symbolRecord](ctx, db.db).SortAsc("ID").List()
	if err != nil {
		return nil, classify("list symbols", err)
	}
	return mapSlice(records, fromSymbolRecord), nil
}

// FindSymbols performs bounded exact, name-prefix, and token-prefix lookup.
func (db *DB) FindSymbols(ctx context.Context, query string, limit int) ([]graph.Symbol, bool, error) {
	if err := validLimit(limit); err != nil {
		return nil, false, err
	}
	normalized := graph.NormalizeName(query)
	if normalized == "" {
		return nil, false, fmt.Errorf("%w: symbol query is empty", ErrInvalid)
	}
	candidateLimit := limit*4 + 1
	byID := map[string]graph.Symbol{}
	if symbol, ok, err := db.Symbol(ctx, query); err != nil {
		return nil, false, err
	} else if ok {
		byID[symbol.ID] = symbol
	}
	stableRecords, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterEqual("StableName", query).Limit(limit + 1).List()
	if err != nil {
		return nil, false, classify("search stable symbol names", err)
	}
	for _, record := range stableRecords {
		byID[record.ID] = fromSymbolRecord(record)
	}

	nameRecords, err := bstore.QueryDB[symbolRecord](ctx, db.db).
		FilterGreaterEqual("NormalizedName", normalized).
		FilterLess("NormalizedName", prefixEnd(normalized)).
		SortAsc("NormalizedName", "ID").Limit(candidateLimit).List()
	if err != nil {
		return nil, false, classify("search symbol names", err)
	}
	for _, record := range nameRecords {
		byID[record.ID] = fromSymbolRecord(record)
	}

	postings, err := bstore.QueryDB[tokenRecord](ctx, db.db).
		FilterGreaterEqual("Token", normalized).
		FilterLess("Token", prefixEnd(normalized)).
		SortAsc("Token", "SymbolID").Limit(candidateLimit).List()
	if err != nil {
		return nil, false, classify("search symbol tokens", err)
	}
	for _, posting := range postings {
		if _, exists := byID[posting.SymbolID]; exists {
			continue
		}
		symbol, ok, err := db.Symbol(ctx, posting.SymbolID)
		if err != nil {
			return nil, false, err
		}
		if ok {
			byID[symbol.ID] = symbol
		}
	}

	results := make([]graph.Symbol, 0, len(byID))
	for _, symbol := range byID {
		results = append(results, symbol)
	}
	slices.SortFunc(results, func(a, b graph.Symbol) int {
		rank := func(symbol graph.Symbol) int {
			switch {
			case symbol.ID == query || symbol.StableName == query:
				return 0
			case symbol.NormalizedName == normalized:
				return 1
			case strings.HasPrefix(symbol.NormalizedName, normalized):
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
	truncated := len(stableRecords) > limit || len(nameRecords) == candidateLimit || len(postings) == candidateLimit || len(results) > limit
	if len(results) > limit {
		results = results[:limit]
	}
	return results, truncated, nil
}

// Occurrences returns bounded occurrences for a symbol in source order.
func (db *DB) Occurrences(ctx context.Context, symbolID string, roles []string, limit int) ([]graph.Occurrence, bool, error) {
	if err := validLimit(limit); err != nil {
		return nil, false, err
	}
	query := bstore.QueryDB[occurrenceRecord](ctx, db.db).FilterEqual("SymbolID", symbolID)
	if len(roles) > 0 {
		values := make([]any, len(roles))
		for i := range roles {
			values[i] = roles[i]
		}
		query.FilterEqual("Role", values...)
	}
	records, err := query.SortAsc("DocumentID", "ID").Limit(limit + 1).List()
	if err != nil {
		return nil, false, classify("list occurrences", err)
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	return mapSlice(records, fromOccurrenceRecord), truncated, nil
}

// EdgesFrom returns bounded forward adjacency in canonical edge order.
func (db *DB) EdgesFrom(ctx context.Context, symbolID string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return db.edges(ctx, "From", symbolID, kinds, limit)
}

// EdgesTo returns bounded reverse adjacency in canonical edge order.
func (db *DB) EdgesTo(ctx context.Context, symbolID string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return db.edges(ctx, "To", symbolID, kinds, limit)
}

func (db *DB) edges(ctx context.Context, direction, symbolID string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	if err := validLimit(limit); err != nil {
		return nil, false, err
	}
	query := bstore.QueryDB[edgeRecord](ctx, db.db).FilterEqual(direction, symbolID)
	if len(kinds) > 0 {
		values := make([]any, len(kinds))
		for i := range kinds {
			values[i] = string(kinds[i])
		}
		query.FilterEqual("Kind", values...)
	}
	other := "To"
	if direction == "To" {
		other = "From"
	}
	records, err := query.SortAsc("Kind", other, "ID").Limit(limit + 1).List()
	if err != nil {
		return nil, false, classify("read adjacency", err)
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	edges := mapSlice(records, fromEdgeRecord)
	slices.SortFunc(edges, graph.CompareEdges)
	return edges, truncated, nil
}

// Export returns every logical fact in deterministic order.
func (db *DB) Export(ctx context.Context) (graph.Snapshot, error) {
	units, err := bstore.QueryDB[unitRecord](ctx, db.db).List()
	if err != nil {
		return graph.Snapshot{}, classify("export units", err)
	}
	documents, err := bstore.QueryDB[documentRecord](ctx, db.db).List()
	if err != nil {
		return graph.Snapshot{}, classify("export documents", err)
	}
	symbols, err := bstore.QueryDB[symbolRecord](ctx, db.db).List()
	if err != nil {
		return graph.Snapshot{}, classify("export symbols", err)
	}
	occurrences, err := bstore.QueryDB[occurrenceRecord](ctx, db.db).List()
	if err != nil {
		return graph.Snapshot{}, classify("export occurrences", err)
	}
	edges, err := bstore.QueryDB[edgeRecord](ctx, db.db).List()
	if err != nil {
		return graph.Snapshot{}, classify("export edges", err)
	}
	snapshot := graph.Snapshot{
		Schema: graph.SchemaVersion,
		Units:  mapSlice(units, fromUnitRecord), Documents: mapSlice(documents, fromDocumentRecord),
		Symbols: mapSlice(symbols, fromSymbolRecord), Occurrences: mapSlice(occurrences, fromOccurrenceRecord),
		Edges: mapSlice(edges, fromEdgeRecord),
	}
	graph.SortSnapshot(&snapshot)
	return snapshot, nil
}

// Issue is one deterministic logical-integrity failure.
type Issue struct {
	Severity string      `json:"severity"`
	Kind     string      `json:"kind"`
	Record   string      `json:"record"`
	Detail   string      `json:"detail"`
	Document string      `json:"document,omitempty"`
	Range    graph.Range `json:"range"`
}

const (
	IssueWarning = "warning"
	IssueError   = "error"
)

// Fatal reports whether an integrity issue makes a graph unsafe for CI policy
// evaluation. Open-world references are diagnostic; ownership damage is fatal.
func (issue Issue) Fatal() bool { return issue.Severity != IssueWarning }

// Verify checks normalized cross-record invariants.
func (db *DB) Verify(ctx context.Context) ([]Issue, error) {
	snapshot, err := db.Export(ctx)
	if err != nil {
		return nil, err
	}
	units := map[string]struct{}{}
	documents := map[string]graph.Document{}
	symbols := map[string]struct{}{}
	for _, unit := range snapshot.Units {
		units[unit.ID] = struct{}{}
	}
	for _, document := range snapshot.Documents {
		documents[document.ID] = document
	}
	for _, symbol := range snapshot.Symbols {
		symbols[symbol.ID] = struct{}{}
	}
	var issues []Issue
	for _, document := range snapshot.Documents {
		if _, ok := units[document.UnitID]; !ok {
			issues = append(issues, Issue{Severity: IssueError, Kind: "orphan-document", Record: document.ID, Detail: "unit " + document.UnitID + " is absent", Document: document.Path})
		}
	}
	for _, symbol := range snapshot.Symbols {
		if _, ok := units[symbol.UnitID]; !ok {
			issue := Issue{Severity: IssueError, Kind: "orphan-symbol", Record: symbol.ID, Detail: "unit " + symbol.UnitID + " is absent", Range: symbol.Definition}
			if document, ok := documents[symbol.DocumentID]; ok {
				issue.Document = document.Path
			}
			issues = append(issues, issue)
		}
		if symbol.DocumentID != "" {
			if document, ok := documents[symbol.DocumentID]; !ok || document.UnitID != symbol.UnitID {
				issue := Issue{Severity: IssueError, Kind: "invalid-symbol-document", Record: symbol.ID, Detail: "document " + symbol.DocumentID + " is absent or owned by another unit", Range: symbol.Definition}
				if ok {
					issue.Document = document.Path
				}
				issues = append(issues, issue)
			}
		}
	}
	for _, occurrence := range snapshot.Occurrences {
		if document, ok := documents[occurrence.DocumentID]; !ok || document.UnitID != occurrence.UnitID {
			issue := Issue{Severity: IssueError, Kind: "invalid-occurrence-document", Record: occurrence.ID, Detail: "document " + occurrence.DocumentID + " is absent or owned by another unit", Range: occurrence.Range}
			if ok {
				issue.Document = document.Path
			}
			issues = append(issues, issue)
		}
		if _, ok := symbols[occurrence.SymbolID]; !ok {
			issues = append(issues, Issue{Severity: IssueWarning, Kind: "unresolved-occurrence", Record: occurrence.ID, Detail: "symbol " + occurrence.SymbolID + " is not indexed (external or builtin symbols may be intentionally unmaterialized)", Document: documents[occurrence.DocumentID].Path, Range: occurrence.Range})
		}
	}
	for _, edge := range snapshot.Edges {
		if _, ok := units[edge.UnitID]; !ok {
			issue := Issue{Severity: IssueError, Kind: "orphan-edge", Record: edge.ID, Detail: "unit " + edge.UnitID + " is absent", Range: edge.Range}
			if document, ok := documents[edge.DocumentID]; ok {
				issue.Document = document.Path
			}
			issues = append(issues, issue)
		}
		if edge.DocumentID != "" {
			if document, ok := documents[edge.DocumentID]; !ok || document.UnitID != edge.UnitID {
				issue := Issue{Severity: IssueError, Kind: "invalid-edge-document", Record: edge.ID, Detail: "document " + edge.DocumentID + " is absent or owned by another unit", Range: edge.Range}
				if ok {
					issue.Document = document.Path
				}
				issues = append(issues, issue)
			}
		}
	}
	slices.SortFunc(issues, func(a, b Issue) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Record, b.Record)
	})
	return issues, nil
}

// Compact rewrites a closed bstore/bbolt database and atomically replaces it.
func Compact(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: openTimeout})
	if err != nil {
		return classify("open database for compaction", err)
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(path), ".weave-compact-*.db")
	if err != nil {
		return fmt.Errorf("create compact database: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	_ = os.Remove(temporaryPath)
	destination, err := bolt.Open(temporaryPath, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		_ = os.Remove(temporaryPath)
		return classify("open compact destination", err)
	}
	compactErr := bolt.Compact(destination, source, 64<<20)
	closeDestinationErr := destination.Close()
	closeSourceErr := source.Close()
	if compactErr != nil {
		_ = os.Remove(temporaryPath)
		return classify("compact database", compactErr)
	}
	if closeDestinationErr != nil {
		_ = os.Remove(temporaryPath)
		return closeDestinationErr
	}
	if closeSourceErr != nil {
		_ = os.Remove(temporaryPath)
		return closeSourceErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace compact database: %w", err)
	}
	return nil
}

func validLimit(limit int) error {
	if limit <= 0 || limit > 100_000 {
		return fmt.Errorf("%w: limit must be between 1 and 100000", ErrInvalid)
	}
	return nil
}

func prefixEnd(prefix string) string      { return prefix + string(rune(0x10ffff)) }
func tokenID(token, symbol string) string { return token + "\x1f" + symbol }

func classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, bolt.ErrInvalid) || errors.Is(err, bolt.ErrChecksum) || errors.Is(err, bolt.ErrVersionMismatch) || errors.Is(err, bstore.ErrStore) {
		return fmt.Errorf("%s: %w: %v; delete and rebuild this derived index", operation, ErrCorrupt, err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func mapSlice[A, B any](values []A, convert func(A) B) []B {
	result := make([]B, len(values))
	for i := range values {
		result[i] = convert(values[i])
	}
	return result
}

func toUnitRecord(v graph.Unit) unitRecord               { return unitRecord(v) }
func fromUnitRecord(v unitRecord) graph.Unit             { return graph.Unit(v) }
func toDocumentRecord(v graph.Document) documentRecord   { return documentRecord(v) }
func fromDocumentRecord(v documentRecord) graph.Document { return graph.Document(v) }
func toSymbolRecord(v graph.Symbol) symbolRecord {
	return symbolRecord{v.ID, v.UnitID, v.StableName, v.DisplayName, v.NormalizedName, v.Kind, v.DocumentID, v.Definition, v.Provider, string(v.Evidence)}
}
func fromSymbolRecord(v symbolRecord) graph.Symbol {
	return graph.Symbol{ID: v.ID, UnitID: v.UnitID, StableName: v.StableName, DisplayName: v.DisplayName, NormalizedName: v.NormalizedName, Kind: v.Kind, DocumentID: v.DocumentID, Definition: v.Definition, Provider: v.Provider, Evidence: graph.Evidence(v.Evidence)}
}
func toOccurrenceRecord(v graph.Occurrence) occurrenceRecord {
	return occurrenceRecord{v.ID, v.UnitID, v.SymbolID, v.DocumentID, v.Role, v.Range, v.Provider, string(v.Evidence)}
}
func fromOccurrenceRecord(v occurrenceRecord) graph.Occurrence {
	return graph.Occurrence{ID: v.ID, UnitID: v.UnitID, SymbolID: v.SymbolID, DocumentID: v.DocumentID, Role: v.Role, Range: v.Range, Provider: v.Provider, Evidence: graph.Evidence(v.Evidence)}
}
func toEdgeRecord(v graph.Edge) edgeRecord {
	return edgeRecord{v.ID, v.UnitID, v.From, v.To, string(v.Kind), string(v.Evidence), v.DocumentID, v.Range, v.Provider}
}
func fromEdgeRecord(v edgeRecord) graph.Edge {
	return graph.Edge{ID: v.ID, UnitID: v.UnitID, From: v.From, To: v.To, Kind: graph.EdgeKind(v.Kind), Evidence: graph.Evidence(v.Evidence), DocumentID: v.DocumentID, Range: v.Range, Provider: v.Provider}
}
