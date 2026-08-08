// Package storage persists and queries Weave's normalized graph facts.
package storage

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	path     string
	db       *bstore.DB
	internMu sync.RWMutex
	interns  map[uint32]string
	stateMu  sync.RWMutex
}

type metadataRecord struct {
	ID      uint8 `bstore:"typename WeaveMetadata,noauto"`
	Version uint32
}

type generationRecord struct {
	ID         uint8 `bstore:"typename WeaveGeneration,noauto"`
	Generation string
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
	if _, statErr := os.Stat(path); statErr == nil {
		version, found, err := storageVersion(path, timeout)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, logicalCorrupt("storage schema marker is absent")
		}
		if version != StorageSchemaVersion {
			return nil, fmt.Errorf("%w: database has storage version %d, executable supports %d; remove this disposable per-worktree index and run weave index", ErrSchema, version, StorageSchemaVersion)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat database: %w", statErr)
	}
	types := make([]any, 0, len(recordTypes)+1)
	types = append(types, metadataRecord{})
	types = append(types, recordTypes...)
	db, err := bstore.Open(ctx, path, &bstore.Options{MustExist: options.MustExist, Timeout: timeout}, types...)
	if err != nil {
		return nil, classify("open database", err)
	}
	store := &DB{path: path, db: db}
	if err := store.ensureMetadata(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.reloadInterns(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (db *DB) reloadInterns(ctx context.Context) error {
	records, err := bstore.QueryDB[internRecord](ctx, db.db).List()
	if err != nil {
		return classify("load intern dictionary", err)
	}
	values := map[uint32]string{0: ""}
	for _, record := range records {
		values[record.ID] = record.Value
	}
	db.internMu.Lock()
	db.interns = values
	db.internMu.Unlock()
	return nil
}

func (db *DB) internDictionary() map[uint32]string {
	db.internMu.RLock()
	values := db.interns
	db.internMu.RUnlock()
	return values
}

// storageVersion reads the unchanged v1/v2 metadata record through bbolt's
// read-only API. This prevents bstore's automatic registration machinery from
// modifying an unsupported database before Weave rejects it. metadataRecord's
// on-disk shape is deliberately frozen: type-version uvarint, one field bitmap
// byte, then the Version uvarint.
func storageVersion(path string, timeout time.Duration) (uint32, bool, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: timeout})
	if err != nil {
		return 0, false, classify("inspect database schema", err)
	}
	var version uint64
	found := false
	err = db.View(func(tx *bolt.Tx) error {
		typeBucket := tx.Bucket([]byte("WeaveMetadata"))
		if typeBucket == nil {
			return nil
		}
		records := typeBucket.Bucket([]byte("records"))
		if records == nil {
			return nil
		}
		value := records.Get([]byte{1})
		if value == nil {
			return nil
		}
		_, n := binary.Uvarint(value)
		if n <= 0 || len(value) <= n {
			return logicalCorrupt("invalid storage schema record")
		}
		value = value[n+1:]
		parsed, m := binary.Uvarint(value)
		if m <= 0 || parsed > uint64(^uint32(0)) {
			return logicalCorrupt("invalid storage schema version")
		}
		version, found = parsed, true
		return nil
	})
	closeErr := db.Close()
	if err != nil {
		return 0, false, err
	}
	if closeErr != nil {
		return 0, false, closeErr
	}
	return uint32(version), found, nil
}

func (db *DB) ensureMetadata(ctx context.Context) error {
	metadata, err := bstore.QueryDB[metadataRecord](ctx, db.db).FilterID(uint8(1)).Get()
	if err == bstore.ErrAbsent {
		if err := db.db.Insert(ctx, &metadataRecord{ID: 1, Version: StorageSchemaVersion}); err != nil {
			return classify("initialize schema", err)
		}
		return nil
	}
	if err != nil {
		return classify("read schema", err)
	}
	if metadata.Version != StorageSchemaVersion {
		return fmt.Errorf("%w: database has storage version %d, executable supports %d; remove this disposable per-worktree index and run weave index", ErrSchema, metadata.Version, StorageSchemaVersion)
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

// Generation returns the freshness manifest generation atomically associated
// with the stored facts. Empty means legacy, explicitly invalidated, or not
// managed by the freshness lifecycle.
func (db *DB) Generation(ctx context.Context) (string, error) {
	record, err := bstore.QueryDB[generationRecord](ctx, db.db).FilterID(uint8(1)).Get()
	if err == bstore.ErrAbsent {
		return "", nil
	}
	if err != nil {
		return "", classify("read database generation", err)
	}
	return record.Generation, nil
}

// SetGeneration atomically associates the complete current fact set with one
// authoritative freshness generation.
func (db *DB) SetGeneration(ctx context.Context, generation string) error {
	if generation == "" {
		return fmt.Errorf("%w: database generation is empty", ErrInvalid)
	}
	return db.setGeneration(ctx, generation)
}

// InvalidateGeneration prevents any derived aggregate from trusting facts
// while a multi-transaction refresh is in progress.
func (db *DB) InvalidateGeneration(ctx context.Context) error {
	return db.setGeneration(ctx, "")
}

func (db *DB) setGeneration(ctx context.Context, generation string) error {
	record := generationRecord{ID: 1, Generation: generation}
	return classify("write database generation", db.db.Write(ctx, func(tx *bstore.Tx) error {
		current, err := bstore.QueryTx[generationRecord](tx).FilterID(uint8(1)).Get()
		if err == bstore.ErrAbsent {
			return tx.Insert(&record)
		}
		if err != nil {
			return err
		}
		if current.Generation == generation {
			return nil
		}
		return tx.Update(&record)
	}))
}

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
	slices.SortFunc(batches, func(a, b graph.UnitFacts) int { return strings.Compare(a.Unit.ID, b.Unit.ID) })
	slices.Sort(removed)
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
	db.stateMu.Lock()
	defer db.stateMu.Unlock()
	err := classify("replace compilation units", db.db.Write(ctx, func(tx *bstore.Tx) error {
		for _, id := range removed {
			if err := deleteUnit(tx, id); err != nil {
				return err
			}
		}
		for _, facts := range batches {
			if err := deleteUnit(tx, facts.Unit.ID); err != nil {
				return err
			}
		}
		retained := newRetention(tx)
		for _, facts := range batches {
			if err := insertUnit(tx, retained, facts); err != nil {
				return err
			}
		}
		return retained.flush()
	}))
	if err != nil {
		return err
	}
	return db.reloadInterns(ctx)
}

func deleteUnit(tx *bstore.Tx, unitID string) error {
	unit, err := bstore.QueryTx[unitRecord](tx).FilterEqual("StableID", unitID).Get()
	if err == bstore.ErrAbsent {
		return nil
	}
	if err != nil {
		return err
	}
	documents, err := bstore.QueryTx[documentRecord](tx).FilterEqual("Unit", unit.ID).List()
	if err != nil {
		return err
	}
	symbols, err := bstore.QueryTx[symbolRecord](tx).FilterEqual("Unit", unit.ID).List()
	if err != nil {
		return err
	}
	occurrences, err := bstore.QueryTx[occurrenceRecord](tx).FilterEqual("Unit", unit.ID).List()
	if err != nil {
		return err
	}
	edges, err := bstore.QueryTx[edgeRecord](tx).FilterEqual("Unit", unit.ID).List()
	if err != nil {
		return err
	}
	symbolIDs := recordIDs(symbols, func(v symbolRecord) uint64 { return v.ID })
	occurrenceIDs := recordIDs(occurrences, func(v occurrenceRecord) uint64 { return v.ID })
	edgeIDs := recordIDs(edges, func(v edgeRecord) uint64 { return v.ID })
	symbolDetails, err := listSymbolDetails(tx, symbolIDs)
	if err != nil {
		return err
	}
	occurrenceDetails, err := listOccurrenceDetails(tx, occurrenceIDs)
	if err != nil {
		return err
	}
	edgeDetails, err := listEdgeDetails(tx, edgeIDs)
	if err != nil {
		return err
	}

	interns := map[uint32]uint64{}
	addInterns := func(values ...uint32) {
		for _, value := range values {
			if value != 0 {
				interns[value]++
			}
		}
	}
	addInterns(unit.Provider, unit.ProviderVersion, unit.Language)
	for _, record := range documents {
		addInterns(record.Language, record.Provider, record.ProviderVersion)
	}
	for _, record := range symbols {
		addInterns(record.Kind)
	}
	for _, record := range symbolDetails {
		addInterns(record.Provider)
	}
	for _, record := range occurrences {
		addInterns(record.Role)
	}
	for _, record := range occurrenceDetails {
		addInterns(record.Provider)
	}
	for _, record := range edgeDetails {
		addInterns(record.Provider)
	}
	entities := map[uint64]uint64{}
	for _, record := range symbols {
		entities[record.ID]++
	}
	for _, record := range occurrences {
		entities[record.Symbol]++
	}
	for _, record := range edges {
		entities[record.From]++
		entities[record.To]++
	}

	if _, err := bstore.QueryTx[tokenRecord](tx).FilterEqual("Unit", unit.ID).Delete(); err != nil {
		return err
	}
	for _, remove := range []func() error{
		func() error { return deleteSymbolDetails(tx, symbolIDs) },
		func() error { return deleteOccurrenceDetails(tx, occurrenceIDs) },
		func() error { return deleteEdgeDetails(tx, edgeIDs) },
		func() error {
			_, err := bstore.QueryTx[edgeRecord](tx).FilterEqual("Unit", unit.ID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[occurrenceRecord](tx).FilterEqual("Unit", unit.ID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[symbolRecord](tx).FilterEqual("Unit", unit.ID).Delete()
			return err
		},
		func() error {
			_, err := bstore.QueryTx[documentRecord](tx).FilterEqual("Unit", unit.ID).Delete()
			return err
		},
	} {
		if err := remove(); err != nil {
			return err
		}
	}
	if err := tx.Delete(&unit); err != nil {
		return err
	}
	if err := releaseEntities(tx, entities); err != nil {
		return err
	}
	return releaseInterns(tx, interns)
}

func insertUnit(tx *bstore.Tx, retained *retention, facts graph.UnitFacts) error {
	provider, err := retained.intern(facts.Unit.Provider)
	if err != nil {
		return err
	}
	providerVersion, err := retained.intern(facts.Unit.ProviderVersion)
	if err != nil {
		return err
	}
	language, err := retained.intern(facts.Unit.Language)
	if err != nil {
		return err
	}
	unit := unitRecord{StableID: facts.Unit.ID, Provider: provider, ProviderVersion: providerVersion, Language: language, Variant: facts.Unit.Variant, InputFingerprint: facts.Unit.InputFingerprint, SurfaceFingerprint: facts.Unit.SurfaceFingerprint, InventoryDigest: facts.Unit.InventoryDigest}
	if err := tx.Insert(&unit); err != nil {
		return err
	}
	documents := append([]graph.Document(nil), facts.Documents...)
	slices.SortFunc(documents, func(a, b graph.Document) int { return strings.Compare(a.ID, b.ID) })
	documentIDs := make(map[string]uint64, len(documents))
	for _, document := range documents {
		language, err := retained.intern(document.Language)
		if err != nil {
			return err
		}
		provider, err := retained.intern(document.Provider)
		if err != nil {
			return err
		}
		providerVersion, err := retained.intern(document.ProviderVersion)
		if err != nil {
			return err
		}
		record := documentRecord{StableID: document.ID, Unit: unit.ID, UnitStable: facts.Unit.ID, Path: document.Path, Language: language, ContentHash: document.ContentHash, Provider: provider, ProviderVersion: providerVersion}
		if err := tx.Insert(&record); err != nil {
			return err
		}
		documentIDs[document.ID] = record.ID
	}
	symbols := append([]graph.Symbol(nil), facts.Symbols...)
	slices.SortFunc(symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	for _, symbol := range symbols {
		entity, err := retained.entity(symbol.ID)
		if err != nil {
			return err
		}
		kind, err := retained.intern(symbol.Kind)
		if err != nil {
			return err
		}
		provider, err := retained.intern(symbol.Provider)
		if err != nil {
			return err
		}
		evidence, ok := evidenceCode(symbol.Evidence)
		if !ok {
			return fmt.Errorf("invalid symbol evidence %q", symbol.Evidence)
		}
		record := symbolRecord{ID: entity, StableID: symbol.ID, Unit: unit.ID, UnitStable: facts.Unit.ID, StableName: symbol.StableName, DisplayName: symbol.DisplayName, NormalizedName: symbol.NormalizedName, Kind: kind, Document: documentIDs[symbol.DocumentID], DocumentStable: symbol.DocumentID}
		if err := tx.Insert(&record); err != nil {
			return err
		}
		detail := symbolDetailRecord{ID: entity, Definition: symbol.Definition, Provider: provider, Evidence: evidence}
		if err := tx.Insert(&detail); err != nil {
			return err
		}
		for _, token := range graph.Tokens(symbol.DisplayName) {
			posting := tokenRecord{Unit: unit.ID, Token: token, Symbol: entity, SymbolStable: symbol.ID}
			if err := tx.Insert(&posting); err != nil {
				return err
			}
		}
	}
	occurrences := append([]graph.Occurrence(nil), facts.Occurrences...)
	slices.SortFunc(occurrences, func(a, b graph.Occurrence) int { return strings.Compare(a.ID, b.ID) })
	for _, occurrence := range occurrences {
		entity, err := retained.entity(occurrence.SymbolID)
		if err != nil {
			return err
		}
		role, err := retained.intern(occurrence.Role)
		if err != nil {
			return err
		}
		provider, err := retained.intern(occurrence.Provider)
		if err != nil {
			return err
		}
		evidence, ok := evidenceCode(occurrence.Evidence)
		if !ok {
			return fmt.Errorf("invalid occurrence evidence %q", occurrence.Evidence)
		}
		document := documentIDs[occurrence.DocumentID]
		record := occurrenceRecord{StableID: occurrence.ID, Unit: unit.ID, UnitStable: facts.Unit.ID, Symbol: entity, SymbolStable: occurrence.SymbolID, Document: document, DocumentStable: occurrence.DocumentID, Role: role}
		if err := tx.Insert(&record); err != nil {
			return err
		}
		detail := occurrenceDetailRecord{ID: record.ID, Range: occurrence.Range, Provider: provider, Evidence: evidence}
		if err := tx.Insert(&detail); err != nil {
			return err
		}
	}
	edges := append([]graph.Edge(nil), facts.Edges...)
	slices.SortFunc(edges, func(a, b graph.Edge) int { return strings.Compare(a.ID, b.ID) })
	for _, edge := range edges {
		from, err := retained.entity(edge.From)
		if err != nil {
			return err
		}
		to, err := retained.entity(edge.To)
		if err != nil {
			return err
		}
		kind, ok := edgeKindCode(edge.Kind)
		if !ok {
			return fmt.Errorf("invalid edge kind %q", edge.Kind)
		}
		provider, err := retained.intern(edge.Provider)
		if err != nil {
			return err
		}
		evidence, ok := evidenceCode(edge.Evidence)
		if !ok {
			return fmt.Errorf("invalid edge evidence %q", edge.Evidence)
		}
		record := edgeRecord{StableID: edge.ID, Unit: unit.ID, UnitStable: facts.Unit.ID, From: from, To: to, Kind: kind, FromStable: edge.From, ToStable: edge.To, DocumentStable: edge.DocumentID}
		if err := tx.Insert(&record); err != nil {
			return err
		}
		detail := edgeDetailRecord{ID: record.ID, Evidence: evidence, Document: documentIDs[edge.DocumentID], Range: edge.Range, Provider: provider}
		if err := tx.Insert(&detail); err != nil {
			return err
		}
	}
	return nil
}

func recordIDs[T any](records []T, id func(T) uint64) []uint64 {
	result := make([]uint64, len(records))
	for i, record := range records {
		result[i] = id(record)
	}
	return result
}

func listSymbolDetails(tx *bstore.Tx, ids []uint64) ([]symbolDetailRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return bstore.QueryTx[symbolDetailRecord](tx).FilterIDs(ids).List()
}
func listOccurrenceDetails(tx *bstore.Tx, ids []uint64) ([]occurrenceDetailRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return bstore.QueryTx[occurrenceDetailRecord](tx).FilterIDs(ids).List()
}
func listEdgeDetails(tx *bstore.Tx, ids []uint64) ([]edgeDetailRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return bstore.QueryTx[edgeDetailRecord](tx).FilterIDs(ids).List()
}
func deleteSymbolDetails(tx *bstore.Tx, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := bstore.QueryTx[symbolDetailRecord](tx).FilterIDs(ids).Delete()
	return err
}
func deleteOccurrenceDetails(tx *bstore.Tx, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := bstore.QueryTx[occurrenceDetailRecord](tx).FilterIDs(ids).Delete()
	return err
}
func deleteEdgeDetails(tx *bstore.Tx, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := bstore.QueryTx[edgeDetailRecord](tx).FilterIDs(ids).Delete()
	return err
}

// Symbol returns a symbol by stable ID.
func (db *DB) Symbol(ctx context.Context, id string) (graph.Symbol, bool, error) {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	record, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterEqual("StableID", id).Get()
	if err == bstore.ErrAbsent {
		return graph.Symbol{}, false, nil
	}
	if err != nil {
		return graph.Symbol{}, false, classify("get symbol", err)
	}
	values, err := db.hydrateSymbols(ctx, []symbolRecord{record})
	if err != nil {
		return graph.Symbol{}, false, classify("hydrate symbol", err)
	}
	return values[0], true, nil
}

// Document returns a document by stable ID.
func (db *DB) Document(ctx context.Context, id string) (graph.Document, bool, error) {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	record, err := bstore.QueryDB[documentRecord](ctx, db.db).FilterEqual("StableID", id).Get()
	if err == bstore.ErrAbsent {
		return graph.Document{}, false, nil
	}
	if err != nil {
		return graph.Document{}, false, classify("get document", err)
	}
	values, err := db.hydrateDocuments(ctx, []documentRecord{record})
	if err != nil {
		return graph.Document{}, false, classify("hydrate document", err)
	}
	return values[0], true, nil
}

// Symbols returns all symbols in canonical order. This is intended for export
// and verification; user-facing searches should use FindSymbols.
func (db *DB) Symbols(ctx context.Context) ([]graph.Symbol, error) {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	records, err := bstore.QueryDB[symbolRecord](ctx, db.db).SortAsc("StableID").List()
	if err != nil {
		return nil, classify("list symbols", err)
	}
	values, err := db.hydrateSymbols(ctx, records)
	return values, classify("hydrate symbols", err)
}

// ScanHotFacts visits the compact projection used by machine-wide symbol
// search and graph traversal. It excludes units, standalone documents,
// occurrences, and source text; the public Symbol/Edge values still hydrate
// their required evidence from cold detail records in bounded batches.
// Callbacks run in deterministic stable fact-ID order, must not reenter this DB,
// and must not retain bstore transaction state. Aggregate ingestion is
// order-independent and normalizes its own indexes.
func (db *DB) ScanHotFacts(ctx context.Context, symbol func(graph.Symbol) error, edge func(graph.Edge) error) error {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	if symbol == nil || edge == nil {
		return fmt.Errorf("%w: hot-fact callbacks are required", ErrInvalid)
	}
	const batchSize = 1_024
	var lastSymbol string
	for {
		records, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterGreater("StableID", lastSymbol).SortAsc("StableID").Limit(batchSize).List()
		if err != nil {
			return classify("scan symbols", err)
		}
		if len(records) == 0 {
			break
		}
		values, err := db.hydrateSymbols(ctx, records)
		if err != nil {
			return classify("hydrate symbol scan", err)
		}
		for _, value := range values {
			if err := symbol(value); err != nil {
				return err
			}
		}
		lastSymbol = records[len(records)-1].StableID
	}
	var lastEdge string
	for {
		records, err := bstore.QueryDB[edgeRecord](ctx, db.db).FilterGreater("StableID", lastEdge).SortAsc("StableID").Limit(batchSize).List()
		if err != nil {
			return classify("scan edges", err)
		}
		if len(records) == 0 {
			break
		}
		values, err := db.hydrateEdges(ctx, records)
		if err != nil {
			return classify("hydrate edge scan", err)
		}
		for _, value := range values {
			if err := edge(value); err != nil {
				return err
			}
		}
		lastEdge = records[len(records)-1].StableID
	}
	return nil
}

// FindSymbols performs bounded exact, name-prefix, and token-prefix lookup.
func (db *DB) FindSymbols(ctx context.Context, query string, limit int) ([]graph.Symbol, bool, error) {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	if err := validLimit(limit); err != nil {
		return nil, false, err
	}
	normalized := graph.NormalizeName(query)
	if normalized == "" {
		return nil, false, fmt.Errorf("%w: symbol query is empty", ErrInvalid)
	}
	candidateLimit := limit*4 + 1
	byID := map[uint64]symbolRecord{}
	if record, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterEqual("StableID", query).Get(); err == nil {
		byID[record.ID] = record
	} else if err != bstore.ErrAbsent {
		return nil, false, classify("get exact symbol", err)
	}
	stableRecords, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterEqual("StableName", query).SortAsc("StableID").Limit(limit + 1).List()
	if err != nil {
		return nil, false, classify("search stable symbol names", err)
	}
	for _, record := range stableRecords {
		byID[record.ID] = record
	}

	nameRecords, err := bstore.QueryDB[symbolRecord](ctx, db.db).
		FilterGreaterEqual("NormalizedName", normalized).
		FilterLess("NormalizedName", prefixEnd(normalized)).
		SortAsc("NormalizedName", "StableID").Limit(candidateLimit).List()
	if err != nil {
		return nil, false, classify("search symbol names", err)
	}
	for _, record := range nameRecords {
		byID[record.ID] = record
	}

	postings, err := bstore.QueryDB[tokenRecord](ctx, db.db).
		FilterGreaterEqual("Token", normalized).
		FilterLess("Token", prefixEnd(normalized)).
		SortAsc("Token", "SymbolStable").Limit(candidateLimit).List()
	if err != nil {
		return nil, false, classify("search symbol tokens", err)
	}
	for _, posting := range postings {
		if _, exists := byID[posting.Symbol]; exists {
			continue
		}
		record, err := bstore.QueryDB[symbolRecord](ctx, db.db).FilterID(posting.Symbol).Get()
		if err != nil && err != bstore.ErrAbsent {
			return nil, false, classify("get token symbol", err)
		}
		if err == nil {
			byID[record.ID] = record
		}
	}

	records := make([]symbolRecord, 0, len(byID))
	for _, record := range byID {
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b symbolRecord) int {
		rank := func(symbol symbolRecord) int {
			switch {
			case symbol.StableID == query || symbol.StableName == query:
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
		return strings.Compare(a.StableID, b.StableID)
	})
	truncated := len(stableRecords) > limit || len(nameRecords) == candidateLimit || len(postings) == candidateLimit || len(records) > limit
	if len(records) > limit {
		records = records[:limit]
	}
	results, err := db.hydrateSymbols(ctx, records)
	if err != nil {
		return nil, false, classify("hydrate symbol search", err)
	}
	return results, truncated, nil
}

// Occurrences returns bounded occurrences for a symbol in source order.
func (db *DB) Occurrences(ctx context.Context, symbolID string, roles []string, limit int) ([]graph.Occurrence, bool, error) {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	if err := validLimit(limit); err != nil {
		return nil, false, err
	}
	entity, err := bstore.QueryDB[entityRecord](ctx, db.db).FilterEqual("StableID", symbolID).Get()
	if err == bstore.ErrAbsent {
		return []graph.Occurrence{}, false, nil
	}
	if err != nil {
		return nil, false, classify("resolve occurrence symbol", err)
	}
	query := bstore.QueryDB[occurrenceRecord](ctx, db.db).FilterEqual("Symbol", entity.ID)
	if len(roles) > 0 {
		values := make([]any, 0, len(roles))
		for _, role := range roles {
			id, ok, err := db.internID(ctx, role)
			if err != nil {
				return nil, false, classify("resolve occurrence role", err)
			}
			if ok {
				values = append(values, id)
			}
		}
		if len(values) == 0 {
			return []graph.Occurrence{}, false, nil
		}
		query.FilterEqual("Role", values...)
	}
	records, err := query.SortAsc("DocumentStable", "StableID").Limit(limit + 1).List()
	if err != nil {
		return nil, false, classify("list occurrences", err)
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	values, err := db.hydrateOccurrences(ctx, records)
	if err != nil {
		return nil, false, classify("hydrate occurrences", err)
	}
	return values, truncated, nil
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
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	if err := validLimit(limit); err != nil {
		return nil, false, err
	}
	entity, err := bstore.QueryDB[entityRecord](ctx, db.db).FilterEqual("StableID", symbolID).Get()
	if err == bstore.ErrAbsent {
		return []graph.Edge{}, false, nil
	}
	if err != nil {
		return nil, false, classify("resolve adjacency symbol", err)
	}
	query := bstore.QueryDB[edgeRecord](ctx, db.db).FilterEqual(direction, entity.ID)
	if len(kinds) > 0 {
		values := make([]any, len(kinds))
		for i := range kinds {
			code, ok := edgeKindCode(kinds[i])
			if !ok {
				return nil, false, fmt.Errorf("%w: invalid edge kind %q", ErrInvalid, kinds[i])
			}
			values[i] = code
		}
		query.FilterEqual("Kind", values...)
	}
	other := "ToStable"
	if direction == "To" {
		other = "FromStable"
	}
	records, err := query.SortAsc("Kind", other, "StableID").Limit(limit + 1).List()
	if err != nil {
		return nil, false, classify("read adjacency", err)
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	edges, err := db.hydrateEdges(ctx, records)
	if err != nil {
		return nil, false, classify("hydrate adjacency", err)
	}
	slices.SortFunc(edges, graph.CompareEdges)
	return edges, truncated, nil
}

// Export returns every logical fact in deterministic order.
func (db *DB) Export(ctx context.Context) (graph.Snapshot, error) {
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	return db.exportUnlocked(ctx)
}

func (db *DB) exportUnlocked(ctx context.Context) (graph.Snapshot, error) {
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
	unitValues, err := db.hydrateUnits(ctx, units)
	if err != nil {
		return graph.Snapshot{}, classify("hydrate export units", err)
	}
	documentValues, err := db.hydrateDocuments(ctx, documents)
	if err != nil {
		return graph.Snapshot{}, classify("hydrate export documents", err)
	}
	symbolValues, err := db.hydrateSymbols(ctx, symbols)
	if err != nil {
		return graph.Snapshot{}, classify("hydrate export symbols", err)
	}
	occurrenceValues, err := db.hydrateOccurrences(ctx, occurrences)
	if err != nil {
		return graph.Snapshot{}, classify("hydrate export occurrences", err)
	}
	edgeValues, err := db.hydrateEdges(ctx, edges)
	if err != nil {
		return graph.Snapshot{}, classify("hydrate export edges", err)
	}
	snapshot := graph.Snapshot{
		Schema: graph.SchemaVersion,
		Units:  unitValues, Documents: documentValues, Symbols: symbolValues,
		Occurrences: occurrenceValues, Edges: edgeValues,
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
	db.stateMu.RLock()
	defer db.stateMu.RUnlock()
	storageIssues, err := db.verifyStorage(ctx)
	if err != nil || len(storageIssues) != 0 {
		return storageIssues, err
	}
	snapshot, err := db.exportUnlocked(ctx)
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
		if _, ok := symbols[occurrence.SymbolID]; !ok && !intentionallyOpenEndpoint(occurrence.SymbolID) {
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

func intentionallyOpenEndpoint(id string) bool {
	return strings.HasPrefix(id, "external:") || strings.HasPrefix(id, "go-external:") || strings.HasPrefix(id, "open:") || strings.HasPrefix(id, "schema-open:")
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

func prefixEnd(prefix string) string { return prefix + string(rune(0x10ffff)) }

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
