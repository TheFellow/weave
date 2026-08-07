// Package catalog manages the explicit per-user registry of local repository indexes.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/repository"
	"github.com/mjl-/bstore"
)

const Schema = "weave.catalog/v1"

var (
	ErrInvalid  = errors.New("invalid catalog operation")
	ErrNotFound = errors.New("catalog repository not found")
)

// Entry is one explicitly registered Git worktree. Root and DatabasePath are
// locations; Identity is the canonical source identity.
type Entry struct {
	Key          string `json:"key"`
	Identity     string `json:"identity"`
	WorktreeID   string `json:"worktree_id"`
	Root         string `json:"root"`
	DatabasePath string `json:"database_path"`
	Remote       string `json:"remote,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Tree         string `json:"tree,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Detached     bool   `json:"detached"`
	Dirty        bool   `json:"dirty"`
	Missing      bool   `json:"missing"`
	Stale        bool   `json:"stale"`
	Diagnostic   string `json:"diagnostic,omitempty"`
}

type metadata struct {
	ID     uint8 `bstore:"typename WeaveCatalogMetadata,noauto"`
	Schema string
}

type entryRecord struct {
	Key          string `bstore:"typename WeaveCatalogEntry"`
	Identity     string `bstore:"index"`
	WorktreeID   string
	Root         string `bstore:"unique"`
	DatabasePath string
	Remote       string
	Commit       string
	Tree         string
	Branch       string
	Detached     bool
	Dirty        bool
}

type DB struct {
	db *bstore.DB
}

// DefaultPath returns the platform application-state catalog path. An explicit
// override must be absolute so invocation location cannot silently change it.
func DefaultPath(override string) (string, error) {
	if override == "" {
		override = os.Getenv("WEAVE_CATALOG")
	}
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("%w: catalog path must be absolute", ErrInvalid)
		}
		return filepath.Clean(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, err = os.UserConfigDir()
		}
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	default:
		base = os.Getenv("XDG_STATE_HOME")
		if base != "" && !filepath.IsAbs(base) {
			base = ""
		}
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
	}
	if err != nil {
		return "", fmt.Errorf("resolve user state: %w", err)
	}
	return filepath.Join(base, "weave", "catalog.db"), nil
}

func Open(ctx context.Context, path string, timeout time.Duration) (*DB, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: catalog path must be absolute", ErrInvalid)
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}
	db, err := bstore.Open(ctx, path, &bstore.Options{Timeout: timeout}, metadata{}, entryRecord{})
	if err != nil {
		return nil, fmt.Errorf("open catalog (another weave process may hold the lock): %w", err)
	}
	catalog := &DB{db: db}
	record, err := bstore.QueryDB[metadata](ctx, db).FilterID(uint8(1)).Get()
	if err == bstore.ErrAbsent {
		err = db.Insert(ctx, &metadata{ID: 1, Schema: Schema})
	}
	if err == nil && record.Schema != "" && record.Schema != Schema {
		err = fmt.Errorf("unsupported catalog schema %q", record.Schema)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return catalog, nil
}

func (db *DB) Close() error {
	if db == nil || db.db == nil {
		return nil
	}
	err := db.db.Close()
	db.db = nil
	return err
}

// Add discovers and atomically inserts or refreshes one explicit worktree.
func (db *DB) Add(ctx context.Context, directory string) (Entry, error) {
	repo, err := repository.Discover(ctx, directory)
	if err != nil {
		return Entry{}, err
	}
	state, err := repo.Inspect(ctx)
	if err != nil {
		return Entry{}, err
	}
	record := fromRepository(repo, state)
	err = db.db.Write(ctx, func(tx *bstore.Tx) error {
		old, getErr := bstore.QueryTx[entryRecord](tx).FilterID(record.Key).Get()
		switch getErr {
		case nil:
			_ = old
			return tx.Update(&record)
		case bstore.ErrAbsent:
			return tx.Insert(&record)
		default:
			return getErr
		}
	})
	if err != nil {
		return Entry{}, fmt.Errorf("register repository: %w", err)
	}
	return toEntry(record), nil
}

// Remove deletes entries selected by exact key, identity, or canonical root.
func (db *DB) Remove(ctx context.Context, selector string) (int, error) {
	if selector == "" {
		return 0, fmt.Errorf("%w: repository selector is empty", ErrInvalid)
	}
	original := selector
	rootSelector := ""
	if filepath.IsAbs(selector) {
		rootSelector = filepath.Clean(selector)
	}
	removed, err := bstore.QueryDB[entryRecord](ctx, db.db).FilterFn(func(record entryRecord) bool {
		return record.Key == original || record.Identity == original || (rootSelector != "" && filepath.Clean(record.Root) == rootSelector)
	}).Delete()
	if err != nil {
		return 0, fmt.Errorf("remove repository: %w", err)
	}
	if removed == 0 {
		return 0, fmt.Errorf("%w: %s", ErrNotFound, original)
	}
	return removed, nil
}

// List returns canonical entries and live missing/stale diagnostics without mutation.
func (db *DB) List(ctx context.Context) ([]Entry, error) {
	records, err := bstore.QueryDB[entryRecord](ctx, db.db).SortAsc("Identity", "Key").List()
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	entries := make([]Entry, 0, len(records))
	for _, record := range records {
		entry := toEntry(record)
		if _, err := os.Stat(entry.Root); err != nil {
			entry.Missing, entry.Diagnostic = errors.Is(err, os.ErrNotExist), err.Error()
			entries = append(entries, entry)
			continue
		}
		repo, err := repository.Discover(ctx, entry.Root)
		if err != nil || repo.Identity != entry.Identity || repo.WorktreeID != entry.WorktreeID {
			entry.Stale = true
			if err != nil {
				entry.Diagnostic = err.Error()
			} else {
				entry.Diagnostic = "registered repository identity or worktree changed"
			}
		} else if state, inspectErr := repo.Inspect(ctx); inspectErr != nil {
			entry.Stale, entry.Diagnostic = true, inspectErr.Error()
		} else if state.Commit != entry.Commit || state.Tree != entry.Tree || state.Branch != entry.Branch || state.Detached != entry.Detached || (len(state.Changes) != 0) != entry.Dirty {
			entry.Stale, entry.Diagnostic = true, "registered Git state changed; run weave repos sync after refreshing the index"
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Sync refreshes selected entries from their exact registered roots. Failures
// are returned as diagnostics while healthy entries commit atomically together.
func (db *DB) Sync(ctx context.Context, selectors []string) ([]Entry, []string, error) {
	records, err := bstore.QueryDB[entryRecord](ctx, db.db).List()
	if err != nil {
		return nil, nil, err
	}
	selected := func(record entryRecord) bool {
		if len(selectors) == 0 {
			return true
		}
		return slices.Contains(selectors, record.Key) || slices.Contains(selectors, record.Identity) || slices.Contains(selectors, record.Root)
	}
	var updates []entryRecord
	var diagnostics []string
	for _, record := range records {
		if !selected(record) {
			continue
		}
		repo, discoverErr := repository.Discover(ctx, record.Root)
		if discoverErr != nil {
			diagnostics = append(diagnostics, record.Identity+": "+discoverErr.Error())
			continue
		}
		state, inspectErr := repo.Inspect(ctx)
		if inspectErr != nil {
			diagnostics = append(diagnostics, record.Identity+": "+inspectErr.Error())
			continue
		}
		updates = append(updates, fromRepository(repo, state))
	}
	if len(updates) > 0 {
		err = db.db.Write(ctx, func(tx *bstore.Tx) error {
			for i := range updates {
				if err := tx.Update(&updates[i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, diagnostics, fmt.Errorf("sync repositories: %w", err)
		}
	}
	slices.Sort(diagnostics)
	entries := make([]Entry, len(updates))
	for i := range updates {
		entries[i] = toEntry(updates[i])
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	return entries, diagnostics, nil
}

func fromRepository(repo repository.Repository, state repository.State) entryRecord {
	return entryRecord{
		Key: repo.Identity + "\x00" + repo.WorktreeID, Identity: repo.Identity,
		WorktreeID: repo.WorktreeID, Root: repo.Root,
		DatabasePath: filepath.Join(repo.StorageDir, "index.db"), Remote: repo.Remote,
		Commit: state.Commit, Tree: state.Tree, Branch: state.Branch,
		Detached: state.Detached, Dirty: len(state.Changes) != 0,
	}
}

func toEntry(record entryRecord) Entry {
	return Entry{
		Key: record.Key, Identity: record.Identity, WorktreeID: record.WorktreeID,
		Root: record.Root, DatabasePath: record.DatabasePath, Remote: record.Remote,
		Commit: record.Commit, Tree: record.Tree, Branch: record.Branch,
		Detached: record.Detached, Dirty: record.Dirty,
	}
}
