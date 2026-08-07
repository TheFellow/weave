// Package freshness coordinates query-driven repository indexing.
package freshness

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/storage"
)

const (
	manifestSchema = "weave.freshness/v1"
	defaultTimeout = 2 * time.Second
)

// ProviderID participates in freshness equality.
type ProviderID struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Unit records provider-owned fingerprints for one complete compilation unit.
type Unit struct {
	ID                 string `json:"id"`
	Owner              string `json:"owner,omitempty"`
	InputFingerprint   string `json:"input_fingerprint,omitempty"`
	SurfaceFingerprint string `json:"surface_fingerprint,omitempty"`
	InventoryDigest    string `json:"inventory_digest,omitempty"`
}

// Manifest is published only after a complete successful refresh.
type Manifest struct {
	Schema             string     `json:"schema"`
	Complete           bool       `json:"complete"`
	RepositoryIdentity string     `json:"repository_identity"`
	WorktreeID         string     `json:"worktree_id"`
	Provider           ProviderID `json:"provider"`
	Commit             string     `json:"commit,omitempty"`
	Tree               string     `json:"tree,omitempty"`
	Branch             string     `json:"branch,omitempty"`
	Detached           bool       `json:"detached"`
	OverlayDigest      string     `json:"overlay_digest"`
	Units              []Unit     `json:"units"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// Request supplies exact repository state and the previous complete inventory.
type Request struct {
	Repository repository.Repository
	State      repository.State
	Previous   *Manifest
	Force      bool
}

// Result contains only complete replacement batches plus a complete resulting
// inventory. Omitted inventory units are not inferred to be deleted.
type Result struct {
	Batches []graph.UnitFacts
	Removed []string
	Units   []Unit
}

// Provider supplies complete semantic compilation-unit batches.
type Provider interface {
	ID() ProviderID
	Refresh(context.Context, Request) (Result, error)
}

// EmptyProvider permits lifecycle initialization before a language provider is
// installed. It can only own an empty inventory.
type EmptyProvider struct{}

func (EmptyProvider) ID() ProviderID { return ProviderID{Name: "weave-empty", Version: "1"} }
func (EmptyProvider) Refresh(context.Context, Request) (Result, error) {
	return Result{Units: []Unit{}}, nil
}

// Status is a cheap comparison between current Git state and the last complete
// manifest.
type Status struct {
	Initialized        bool   `json:"initialized"`
	Current            bool   `json:"current"`
	Refreshed          bool   `json:"refreshed,omitempty"`
	Reason             string `json:"reason,omitempty"`
	RepositoryIdentity string `json:"repository_identity"`
	WorktreeID         string `json:"worktree_id"`
	Commit             string `json:"commit,omitempty"`
	Tree               string `json:"tree,omitempty"`
	Dirty              bool   `json:"dirty"`
	ChangeCount        int    `json:"change_count"`
	DatabasePath       string `json:"database_path"`
	ManifestPath       string `json:"manifest_path"`
}

// Manager owns freshness for one directory/repository.
type Manager struct {
	Directory   string
	Provider    Provider
	LockTimeout time.Duration
	Command     string
}

// ProviderID returns the semantic provider identity used in freshness keys.
func (m Manager) ProviderID() ProviderID { return m.provider().ID() }

// Ensure returns a current index, refreshing under a bounded writer lock when
// necessary. Force asks the provider to refresh even if the manifest matches.
func (m Manager) Ensure(ctx context.Context, force bool) (Status, error) {
	repo, state, manifest, status, err := m.inspect(ctx)
	if err != nil {
		return Status{}, err
	}
	if status.Current && !force {
		return status, nil
	}
	lock, err := acquire(ctx, filepath.Join(repo.StorageDir, "refresh.lock"), m.timeout(), m.Command)
	if err != nil {
		return Status{}, err
	}
	defer lock.release()

	// A concurrent invocation may have refreshed while this one waited.
	repo, state, manifest, status, err = m.inspect(ctx)
	if err != nil {
		return Status{}, err
	}
	if status.Current && !force {
		return status, nil
	}
	provider := m.provider()
	result, err := provider.Refresh(ctx, Request{Repository: repo, State: state, Previous: manifest, Force: force})
	if err != nil {
		return Status{}, fmt.Errorf("refresh with provider %s: %w", provider.ID().Name, err)
	}
	if err := validateResult(result, manifest); err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(repo.StorageDir, 0o755); err != nil {
		return Status{}, fmt.Errorf("create Weave storage: %w", err)
	}
	databasePath := filepath.Join(repo.StorageDir, "index.db")
	db, err := storage.Open(ctx, databasePath, storage.Options{})
	if err != nil {
		return Status{}, err
	}
	if err := db.ReplaceUnits(ctx, result.Batches, result.Removed); err != nil {
		_ = db.Close()
		return Status{}, err
	}
	if err := db.Close(); err != nil {
		return Status{}, fmt.Errorf("close refreshed database: %w", err)
	}
	newManifest := buildManifest(repo, state, provider.ID(), result.Units)
	if err := writeManifest(filepath.Join(repo.StorageDir, "manifest.json"), newManifest); err != nil {
		return Status{}, err
	}
	status = statusFor(repo, state, &newManifest, provider.ID())
	status.Refreshed = true
	return status, nil
}

// Inspect reports freshness without changing repository state.
func (m Manager) Inspect(ctx context.Context) (Status, error) {
	_, _, _, status, err := m.inspect(ctx)
	return status, err
}

// DatabasePath resolves the worktree-specific derived graph path.
func (m Manager) DatabasePath(ctx context.Context) (string, error) {
	repo, err := repository.Discover(ctx, m.Directory)
	if err != nil {
		return "", err
	}
	return filepath.Join(repo.StorageDir, "index.db"), nil
}

func (m Manager) inspect(ctx context.Context) (repository.Repository, repository.State, *Manifest, Status, error) {
	repo, err := repository.Discover(ctx, m.Directory)
	if err != nil {
		return repository.Repository{}, repository.State{}, nil, Status{}, err
	}
	state, err := repo.Inspect(ctx)
	if err != nil {
		return repository.Repository{}, repository.State{}, nil, Status{}, err
	}
	path := filepath.Join(repo.StorageDir, "manifest.json")
	manifest, err := readManifest(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Invalid/truncated derived state is stale and deterministically rebuilt.
		manifest = nil
	}
	status := statusFor(repo, state, manifest, m.provider().ID())
	return repo, state, manifest, status, nil
}

func statusFor(repo repository.Repository, state repository.State, manifest *Manifest, provider ProviderID) Status {
	status := Status{
		Initialized: manifest != nil, RepositoryIdentity: repo.Identity, WorktreeID: repo.WorktreeID,
		Commit: state.Commit, Tree: state.Tree, Dirty: len(state.Changes) != 0, ChangeCount: len(state.Changes),
		DatabasePath: filepath.Join(repo.StorageDir, "index.db"), ManifestPath: filepath.Join(repo.StorageDir, "manifest.json"),
	}
	if manifest == nil {
		status.Reason = "index is not initialized"
		return status
	}
	if manifest.Schema != manifestSchema || !manifest.Complete {
		status.Reason = "freshness manifest is incomplete or unsupported"
		return status
	}
	if manifest.RepositoryIdentity != repo.Identity || manifest.WorktreeID != repo.WorktreeID {
		status.Reason = "repository or worktree identity changed"
		return status
	}
	if manifest.Provider != provider {
		status.Reason = "provider changed"
		return status
	}
	if manifest.Commit != state.Commit || manifest.Tree != state.Tree || manifest.Branch != state.Branch || manifest.Detached != state.Detached || manifest.OverlayDigest != overlayDigest(state) {
		status.Reason = "repository state changed"
		return status
	}
	if _, err := os.Stat(status.DatabasePath); err != nil {
		status.Reason = "graph database is missing"
		return status
	}
	status.Current = true
	return status
}

func buildManifest(repo repository.Repository, state repository.State, provider ProviderID, units []Unit) Manifest {
	units = append([]Unit(nil), units...)
	slices.SortFunc(units, func(a, b Unit) int { return strings.Compare(a.ID, b.ID) })
	return Manifest{
		Schema: manifestSchema, Complete: true, RepositoryIdentity: repo.Identity, WorktreeID: repo.WorktreeID,
		Provider: provider, Commit: state.Commit, Tree: state.Tree, Branch: state.Branch, Detached: state.Detached,
		OverlayDigest: overlayDigest(state), Units: units, UpdatedAt: time.Now().UTC(),
	}
}

func overlayDigest(state repository.State) string {
	encoded, _ := json.Marshal(state.Changes)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateResult(result Result, previous *Manifest) error {
	prior := map[string]Unit{}
	if previous != nil {
		for _, unit := range previous.Units {
			prior[unit.ID] = unit
		}
	}
	inventory := map[string]bool{}
	units := map[string]Unit{}
	for _, unit := range result.Units {
		if unit.ID == "" || inventory[unit.ID] {
			return fmt.Errorf("invalid provider result: duplicate or empty inventory unit %q", unit.ID)
		}
		inventory[unit.ID] = true
		units[unit.ID] = unit
	}
	removed := map[string]bool{}
	for _, id := range result.Removed {
		if id == "" || removed[id] || inventory[id] {
			return fmt.Errorf("invalid provider result: invalid removed unit %q", id)
		}
		removed[id] = true
	}
	returned := map[string]bool{}
	for _, facts := range result.Batches {
		id := facts.Unit.ID
		if returned[id] || !inventory[id] {
			return fmt.Errorf("invalid provider result: replacement unit %q is duplicate or absent from inventory", id)
		}
		returned[id] = true
	}
	for id, unit := range units {
		if returned[id] {
			continue
		}
		if old, ok := prior[id]; !ok || old != unit {
			return fmt.Errorf("invalid provider result: unit %q has no replacement batch or reusable matching fingerprint", id)
		}
	}
	if previous != nil {
		for _, old := range previous.Units {
			if !inventory[old.ID] && !removed[old.ID] {
				return fmt.Errorf("invalid provider result: previous unit %q omitted without removal", old.ID)
			}
		}
	}
	return nil
}

func readManifest(path string) (*Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode freshness manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("decode freshness manifest: trailing data")
	}
	return &manifest, nil
}

func writeManifest(path string, manifest Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return fmt.Errorf("create manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode freshness manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync freshness manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close freshness manifest: %w", err)
	}
	if runtime.GOOS == "windows" {
		// Windows cannot rename over an existing file. A crash in this small
		// interval yields a missing marker, which is safely treated as stale.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("replace freshness manifest: %w", err)
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish freshness manifest: %w", err)
	}
	return nil
}

func (m Manager) provider() Provider {
	if m.Provider == nil {
		return EmptyProvider{}
	}
	return m.Provider
}

func (m Manager) timeout() time.Duration {
	if m.LockTimeout <= 0 {
		return defaultTimeout
	}
	return m.LockTimeout
}

type lockOwner struct {
	Token   string    `json:"token"`
	PID     int       `json:"pid"`
	Host    string    `json:"host"`
	Command string    `json:"command,omitempty"`
	Started time.Time `json:"started"`
}

type writerLock struct {
	path  string
	owner lockOwner
}

// LockTimeoutError reports the extant writer for operator diagnostics.
type LockTimeoutError struct {
	Path  string
	Owner string
}

func (e *LockTimeoutError) Error() string {
	return fmt.Sprintf("timed out waiting for refresh lock %s (owner: %s)", e.Path, e.Owner)
}

func acquire(ctx context.Context, path string, timeout time.Duration, command string) (*writerLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("create lock token: %w", err)
	}
	host, _ := os.Hostname()
	owner := lockOwner{Token: hex.EncodeToString(tokenBytes), PID: os.Getpid(), Host: host, Command: command, Started: time.Now().UTC()}
	encoded, _ := json.Marshal(owner)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, writeErr := file.Write(append(encoded, '\n'))
			if writeErr == nil {
				writeErr = file.Sync()
			}
			closeErr := file.Close()
			if writeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write refresh lock: %w", writeErr)
			}
			if closeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("close refresh lock: %w", closeErr)
			}
			return &writerLock{path: path, owner: owner}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire refresh lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, &LockTimeoutError{Path: path, Owner: readLockOwner(path)}
		case <-ticker.C:
		}
	}
}

func (lock *writerLock) release() {
	data, err := os.ReadFile(lock.path)
	if err != nil {
		return
	}
	var owner lockOwner
	if json.Unmarshal(data, &owner) == nil && owner.Token == lock.owner.Token {
		_ = os.Remove(lock.path)
	}
}

func readLockOwner(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer file.Close()
	data, _ := io.ReadAll(io.LimitReader(file, 8<<10))
	var owner lockOwner
	if json.Unmarshal(data, &owner) != nil {
		return "invalid metadata"
	}
	return fmt.Sprintf("pid=%d host=%s command=%q started=%s", owner.PID, owner.Host, owner.Command, owner.Started.Format(time.RFC3339))
}
