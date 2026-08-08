// Package aggregate materializes a disposable machine-wide projection for
// catalog symbol search and graph traversal. Per-worktree stores remain the
// sole freshness authorities.
package aggregate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const (
	Schema         = "weave.aggregate/v1"
	defaultTimeout = 2 * time.Second
)

var (
	ErrInvalid = errors.New("invalid machine aggregate")
	ErrStale   = errors.New("stale machine aggregate")
	ErrCorrupt = errors.New("corrupt machine aggregate")
	ErrBusy    = errors.New("machine aggregate is busy")
)

// HotSource is the authoritative storage projection consumed during a rebuild.
type HotSource interface {
	ScanHotFacts(context.Context, func(graph.Symbol) error, func(graph.Edge) error) error
}

// Source identifies one exact authoritative worktree generation.
type Source struct {
	Key          string
	Repository   string
	WorktreeID   string
	Root         string
	DatabasePath string
	Generation   string
	Store        HotSource
	// Release relinquishes the authoritative database read handle after its hot
	// facts have been scanned and before final generation validation.
	Release func() error
	// Validate re-proves the worktree generation after the source handle is
	// released. A mismatch aborts publication.
	Validate func(context.Context) (string, error)
}

// Provenance identifies the worktree that supplied an observed fact.
type Provenance struct {
	Kind       string
	FactID     string
	Repository string
	WorktreeID string
	Root       string
}

// Status describes one validated or newly materialized generation.
type Status struct {
	Path       string
	Generation string
	Sources    int
	Symbols    int
	Edges      int
	Bytes      int64
	Rebuilt    bool
}

type metadataRecord struct {
	ID          uint8 `bstore:"typename WeaveAggregateMetadata,noauto"`
	Schema      string
	GraphSchema uint32
	Generation  string
	Sources     int
	Symbols     int
	Edges       int
}

type sourceRecord struct {
	ID           string `bstore:"typename WeaveAggregateSource"`
	Key          string
	Repository   string
	WorktreeID   string
	Root         string
	DatabasePath string
	Generation   string
}

type symbolRecord struct {
	ID             string `bstore:"typename WeaveAggregateSymbol"`
	FactID         string `bstore:"index"`
	SourceID       string `bstore:"index SourceID+FactID,index SourceID+StableName,index SourceID+NormalizedName"`
	UnitID         string
	StableName     string
	DisplayName    string
	NormalizedName string
	SearchTerms    string
	Kind           string
	DocumentID     string
	Definition     graph.Range
	Provider       string
	Evidence       string
}

type tokenRecord struct {
	ID       string `bstore:"typename WeaveAggregateToken"`
	SourceID string `bstore:"index SourceID+Token+SymbolID"`
	Token    string
	SymbolID string
}

type edgeRecord struct {
	ID         string `bstore:"typename WeaveAggregateEdge"`
	SemanticID string `bstore:"index"`
	SourceID   string `bstore:"index SourceID+From+Kind+To,index SourceID+To+Kind+From"`
	FactID     string
	UnitID     string
	From       string `bstore:"index From+Kind+To"`
	To         string `bstore:"index To+Kind+From"`
	Kind       string
	Evidence   string
	DocumentID string
	Range      graph.Range
	Provider   string
}

type lockRecord struct {
	ID uint8 `bstore:"typename WeaveAggregateLock,noauto"`
}

var records = []any{
	metadataRecord{}, sourceRecord{}, symbolRecord{},
	tokenRecord{}, edgeRecord{},
}

// DB is one immutable validated aggregate generation.
type DB struct {
	path       string
	db         *bstore.DB
	generation string
	sources    map[string]sourceRecord
	sourceIDs  []string
	mu         sync.Mutex
	observed   map[string]Provenance
}

// Ensure validates or completely rebuilds the exact source generation. A
// generation database is immutable after publication, so readers never observe
// a partially rebuilt graph.
func Ensure(ctx context.Context, directory string, sources []Source, timeout time.Duration) (*DB, Status, error) {
	canonical, generation, err := prepareSources(directory, sources)
	if err != nil {
		return nil, Status{}, err
	}
	path := generationPath(directory, generation)
	if db, status, openErr := openGeneration(ctx, path, canonical, generation, timeout); openErr == nil {
		return db, status, nil
	} else if errors.Is(openErr, ErrBusy) {
		return nil, Status{}, openErr
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, Status{}, fmt.Errorf("create aggregate directory: %w", err)
	}
	lock, err := bstore.Open(ctx, filepath.Join(directory, "build.lock"), &bstore.Options{Timeout: timeout}, lockRecord{})
	if err != nil {
		return nil, Status{}, fmt.Errorf("acquire aggregate build lock: %w", err)
	}
	defer lock.Close()
	// Another process may have completed this exact generation while we waited.
	if db, status, openErr := openGeneration(ctx, path, canonical, generation, timeout); openErr == nil {
		return db, status, nil
	} else if errors.Is(openErr, ErrBusy) {
		return nil, Status{}, openErr
	}
	for _, source := range canonical {
		if source.Store == nil {
			return nil, Status{}, fmt.Errorf("%w: source %q has no authoritative store for rebuild", ErrInvalid, source.Key)
		}
		if source.Validate == nil {
			return nil, Status{}, fmt.Errorf("%w: source %q has no post-scan generation validator", ErrInvalid, source.Key)
		}
	}
	status, err := build(ctx, directory, path, canonical, generation)
	if err != nil {
		return nil, Status{}, err
	}
	db, opened, err := openGeneration(ctx, path, canonical, generation, timeout)
	if err != nil {
		return nil, Status{}, fmt.Errorf("open published aggregate: %w", err)
	}
	opened.Rebuilt = true
	opened.Bytes = status.Bytes
	removeOldGenerations(directory, path)
	return db, opened, nil
}

// Open validates an existing exact generation without opening authoritative
// worktree databases. Callers have already established source generations by
// running each worktree's normal freshness check.
func Open(ctx context.Context, directory string, sources []Source, timeout time.Duration) (*DB, Status, error) {
	canonical, generation, err := prepareSources(directory, sources)
	if err != nil {
		return nil, Status{}, err
	}
	return openGeneration(ctx, generationPath(directory, generation), canonical, generation, timeout)
}

// Generation returns the deterministic source-set generation and destination
// filename without touching storage. It is useful for diagnostics and tests.
func Generation(directory string, sources []Source) (string, string, error) {
	_, generation, err := prepareSources(directory, sources)
	if err != nil {
		return "", "", err
	}
	return generation, generationPath(directory, generation), nil
}

func prepareSources(directory string, sources []Source) ([]Source, string, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, "", fmt.Errorf("%w: aggregate directory must be absolute", ErrInvalid)
	}
	if len(sources) == 0 || len(sources) > 256 {
		return nil, "", fmt.Errorf("%w: source count must be between 1 and 256", ErrInvalid)
	}
	canonical := append([]Source(nil), sources...)
	slices.SortFunc(canonical, func(a, b Source) int {
		if a.Repository != b.Repository {
			return strings.Compare(a.Repository, b.Repository)
		}
		if a.Key != b.Key {
			return strings.Compare(a.Key, b.Key)
		}
		return strings.Compare(a.DatabasePath, b.DatabasePath)
	})
	seen := map[string]bool{}
	for _, source := range canonical {
		if source.Key == "" || source.Repository == "" || source.WorktreeID == "" || source.Root == "" || source.DatabasePath == "" || source.Generation == "" {
			return nil, "", fmt.Errorf("%w: incomplete aggregate source %q", ErrInvalid, source.Key)
		}
		if seen[source.Key] {
			return nil, "", fmt.Errorf("%w: duplicate aggregate source %q", ErrInvalid, source.Key)
		}
		seen[source.Key] = true
	}
	type generationSource struct {
		Key, Repository, WorktreeID, Root, DatabasePath, Generation string
	}
	projection := struct {
		Schema      string
		GraphSchema uint32
		Sources     []generationSource
	}{Schema: Schema, GraphSchema: graph.SchemaVersion}
	for _, source := range canonical {
		projection.Sources = append(projection.Sources, generationSource{
			source.Key, source.Repository, source.WorktreeID, filepath.Clean(source.Root),
			filepath.Clean(source.DatabasePath), source.Generation,
		})
	}
	encoded, _ := json.Marshal(projection)
	digest := sha256.Sum256(encoded)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func generationPath(directory, generation string) string {
	return filepath.Join(directory, "graph-"+generation+".db")
}

func openGeneration(ctx context.Context, path string, sources []Source, generation string, timeout time.Duration) (*DB, Status, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	db, err := bstore.Open(ctx, path, &bstore.Options{MustExist: true, Timeout: timeout}, records...)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Status{}, fmt.Errorf("%w: %s", ErrStale, path)
		}
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, Status{}, fmt.Errorf("%w: %s", ErrBusy, path)
		}
		return nil, Status{}, fmt.Errorf("%w: open %s: %v", ErrCorrupt, path, err)
	}
	fail := func(err error) (*DB, Status, error) {
		_ = db.Close()
		return nil, Status{}, err
	}
	metadata, err := bstore.QueryDB[metadataRecord](ctx, db).FilterID(uint8(1)).Get()
	if err != nil {
		return fail(fmt.Errorf("%w: read metadata: %v", ErrCorrupt, err))
	}
	if metadata.Schema != Schema || metadata.GraphSchema != graph.SchemaVersion || metadata.Generation != generation {
		return fail(fmt.Errorf("%w: aggregate metadata does not match requested generation", ErrStale))
	}
	stored, err := bstore.QueryDB[sourceRecord](ctx, db).SortAsc("ID").List()
	if err != nil {
		return fail(fmt.Errorf("%w: read sources: %v", ErrCorrupt, err))
	}
	if len(stored) != len(sources) {
		return fail(fmt.Errorf("%w: aggregate source count changed", ErrStale))
	}
	storedByID := make(map[string]sourceRecord, len(stored))
	for _, source := range stored {
		storedByID[source.ID] = source
	}
	for _, source := range sources {
		want := toSourceRecord(source)
		if storedByID[want.ID] != want {
			return fail(fmt.Errorf("%w: aggregate source %q changed", ErrStale, source.Key))
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	sourceMap := make(map[string]sourceRecord, len(stored))
	for _, source := range stored {
		sourceMap[source.ID] = source
	}
	sourceIDs := make([]string, 0, len(sources))
	for _, source := range sources {
		sourceIDs = append(sourceIDs, sourceIndexID(source.Key))
	}
	return &DB{path: path, db: db, generation: generation, sources: sourceMap, sourceIDs: sourceIDs, observed: map[string]Provenance{}}, Status{
		Path: path, Generation: generation, Sources: metadata.Sources,
		Symbols: metadata.Symbols, Edges: metadata.Edges, Bytes: info.Size(),
	}, nil
}

func build(ctx context.Context, directory, destination string, sources []Source, generation string) (Status, error) {
	temporary, err := os.CreateTemp(directory, ".aggregate-*.db")
	if err != nil {
		return Status{}, fmt.Errorf("create aggregate temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return Status{}, err
	}
	_ = os.Remove(temporaryPath)
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	db, err := bstore.Open(ctx, temporaryPath, &bstore.Options{Timeout: defaultTimeout}, records...)
	if err != nil {
		return Status{}, fmt.Errorf("create aggregate database: %w", err)
	}
	failed := func(operation string, err error) (Status, error) {
		_ = db.Close()
		return Status{}, fmt.Errorf("%s aggregate: %w", operation, err)
	}
	for _, source := range sources {
		record := toSourceRecord(source)
		if err := db.Insert(ctx, &record); err != nil {
			return failed("insert source", err)
		}
		if err := ingestSource(ctx, db, source); err != nil {
			return failed("ingest source "+source.Key, err)
		}
	}
	for _, source := range sources {
		if source.Release != nil {
			if err := source.Release(); err != nil {
				return failed("release source "+source.Key, err)
			}
		}
	}
	for _, source := range sources {
		if source.Validate == nil {
			return failed("revalidate source "+source.Key, fmt.Errorf("%w: post-scan generation validator is unavailable", ErrInvalid))
		}
		generation, err := source.Validate(ctx)
		if err != nil {
			return failed("revalidate source "+source.Key, err)
		}
		if generation != source.Generation {
			return failed("revalidate source "+source.Key, fmt.Errorf("%w: generation changed from %q to %q during materialization", ErrStale, source.Generation, generation))
		}
	}
	// FactID and SemanticID are indexed. Walking those indexes in order keeps
	// metadata counting constant-memory even when the hot projection is large.
	symbols, err := countDistinctSymbols(ctx, db)
	if err != nil {
		return failed("count symbols", err)
	}
	edges, err := countDistinctEdges(ctx, db)
	if err != nil {
		return failed("count edges", err)
	}
	metadata := metadataRecord{ID: 1, Schema: Schema, GraphSchema: graph.SchemaVersion, Generation: generation, Sources: len(sources), Symbols: symbols, Edges: edges}
	if err := db.Insert(ctx, &metadata); err != nil {
		return failed("write metadata", err)
	}
	if err := db.Close(); err != nil {
		return Status{}, fmt.Errorf("close aggregate database: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if err := publish(temporaryPath, destination); err != nil {
		return Status{}, err
	}
	cleanup = false
	info, err := os.Stat(destination)
	if err != nil {
		return Status{}, err
	}
	return Status{Path: destination, Generation: generation, Sources: len(sources), Symbols: symbols, Edges: edges, Bytes: info.Size(), Rebuilt: true}, nil
}

func countDistinctSymbols(ctx context.Context, db *bstore.DB) (int, error) {
	count := 0
	previous := ""
	first := true
	err := bstore.QueryDB[symbolRecord](ctx, db).SortAsc("FactID").ForEach(func(record symbolRecord) error {
		if first || record.FactID != previous {
			count++
			previous = record.FactID
			first = false
		}
		return nil
	})
	return count, err
}

func countDistinctEdges(ctx context.Context, db *bstore.DB) (int, error) {
	count := 0
	previous := ""
	first := true
	err := bstore.QueryDB[edgeRecord](ctx, db).SortAsc("SemanticID").ForEach(func(record edgeRecord) error {
		if first || record.SemanticID != previous {
			count++
			previous = record.SemanticID
			first = false
		}
		return nil
	})
	return count, err
}

func ingestSource(ctx context.Context, db *bstore.DB, source Source) error {
	const batchSize = 4096
	var symbols []graph.Symbol
	var edges []graph.Edge
	flush := func() error {
		if len(symbols) == 0 && len(edges) == 0 {
			return nil
		}
		err := db.Write(ctx, func(tx *bstore.Tx) error {
			for _, symbol := range symbols {
				if err := upsertSymbol(tx, source.Key, symbol); err != nil {
					return err
				}
			}
			for _, edge := range edges {
				if err := upsertEdge(tx, source.Key, edge); err != nil {
					return err
				}
			}
			return nil
		})
		symbols = symbols[:0]
		edges = edges[:0]
		return err
	}
	err := source.Store.ScanHotFacts(ctx, func(symbol graph.Symbol) error {
		symbols = append(symbols, symbol)
		if len(symbols)+len(edges) >= batchSize {
			return flush()
		}
		return nil
	}, func(edge graph.Edge) error {
		edges = append(edges, edge)
		if len(symbols)+len(edges) >= batchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

func upsertSymbol(tx *bstore.Tx, sourceID string, symbol graph.Symbol) error {
	record := toSymbolRecord(sourceID, symbol)
	if err := tx.Insert(&record); err != nil {
		return err
	}
	return insertTokens(tx, record.SourceID, record.ID, symbol)
}

func insertTokens(tx *bstore.Tx, sourceID, symbolID string, symbol graph.Symbol) error {
	for _, token := range graph.SymbolTokens(symbol) {
		record := tokenRecord{ID: sourceID + "\x1f" + token + "\x1f" + symbol.ID, SourceID: sourceID, Token: token, SymbolID: symbolID}
		if err := tx.Insert(&record); err != nil {
			return err
		}
	}
	return nil
}

func upsertEdge(tx *bstore.Tx, sourceID string, edge graph.Edge) error {
	indexedSource := sourceIndexID(sourceID)
	record := toEdgeRecord(indexedSource, edge)
	return tx.Insert(&record)
}

func publish(temporary, destination string) error {
	// Generation filenames are immutable. A pre-existing destination can only
	// be corrupt/incomplete because a valid one was handled before rebuilding.
	backup := destination + ".invalid"
	_ = os.Remove(backup)
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("quarantine invalid aggregate: %w", err)
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Rename(backup, destination)
		return fmt.Errorf("publish aggregate: %w", err)
	}
	_ = os.Remove(backup)
	return nil
}

func removeOldGenerations(directory, current string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(directory, name)
		if !entry.IsDir() && path != current && strings.HasPrefix(name, "graph-") && strings.HasSuffix(name, ".db") {
			_ = os.Remove(path)
		}
		if !entry.IsDir() && strings.HasPrefix(name, ".aggregate-") && strings.HasSuffix(name, ".db") {
			_ = os.Remove(path)
		}
	}
}

func toSourceRecord(source Source) sourceRecord {
	return sourceRecord{sourceIndexID(source.Key), source.Key, source.Repository, source.WorktreeID, filepath.Clean(source.Root), filepath.Clean(source.DatabasePath), source.Generation}
}

func sourceIndexID(sourceID string) string {
	digest := sha256.Sum256([]byte(sourceID))
	return hex.EncodeToString(digest[:])
}

func toSymbolRecord(sourceID string, value graph.Symbol) symbolRecord {
	indexedSource := sourceIndexID(sourceID)
	return symbolRecord{indexedSource + "\x1f" + value.ID, value.ID, indexedSource, value.UnitID, value.StableName, value.DisplayName, value.NormalizedName, strings.Join(value.SearchTerms, "\x00"), value.Kind, value.DocumentID, value.Definition, value.Provider, string(value.Evidence)}
}

func fromSymbolRecord(value symbolRecord) graph.Symbol {
	var searchTerms []string
	if value.SearchTerms != "" {
		searchTerms = strings.Split(value.SearchTerms, "\x00")
	}
	return graph.Symbol{ID: value.FactID, UnitID: value.UnitID, StableName: value.StableName, DisplayName: value.DisplayName, NormalizedName: value.NormalizedName, SearchTerms: searchTerms, Kind: value.Kind, DocumentID: value.DocumentID, Definition: value.Definition, Provider: value.Provider, Evidence: graph.Evidence(value.Evidence)}
}

func toEdgeRecord(sourceID string, value graph.Edge) edgeRecord {
	return edgeRecord{sourceID + "\x1f" + value.ID, edgeIdentity(value), sourceID, value.ID, value.UnitID, value.From, value.To, string(value.Kind), string(value.Evidence), value.DocumentID, value.Range, value.Provider}
}

func fromEdgeRecord(value edgeRecord) graph.Edge {
	return graph.Edge{ID: value.FactID, UnitID: value.UnitID, From: value.From, To: value.To, Kind: graph.EdgeKind(value.Kind), Evidence: graph.Evidence(value.Evidence), DocumentID: value.DocumentID, Range: value.Range, Provider: value.Provider}
}

func edgeIdentity(edge graph.Edge) string {
	value := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d:%d:%d:%d:%d:%d",
		edge.From, edge.Kind, edge.To, edge.Evidence, edge.DocumentID, edge.Provider,
		edge.Range.Start.Line, edge.Range.Start.Column, edge.Range.Start.Byte,
		edge.Range.End.Line, edge.Range.End.Column, edge.Range.End.Byte)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func prefixEnd(prefix string) string { return prefix + string(rune(0x10ffff)) }
