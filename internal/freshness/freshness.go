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
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/storage"
)

const (
	manifestSchema           = "weave.freshness/v1"
	defaultTimeout           = 2 * time.Second
	generationMismatchReason = "graph database generation does not match the freshness manifest"
	// The workspace provider can legitimately own hundreds of thousands of
	// path units. Publication and reading must enforce the same encoded bound.
	maxManifestBytes int64 = 256 << 20
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
	Diagnostics        []string   `json:"diagnostics,omitempty"`
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
	// Diagnostics are deterministic provider degradations that remain visible
	// while this complete manifest is current.
	Diagnostics []string
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
	// Generation is a deterministic digest of the complete authoritative
	// freshness manifest, excluding publication time and diagnostics. It lets
	// disposable read caches prove that they represent this exact worktree
	// generation without treating the cache as a freshness authority.
	Generation  string   `json:"generation,omitempty"`
	Diagnostics []string `json:"diagnostics,omitempty"`
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
		matches, matchErr := databaseGenerationMatches(ctx, status, m.timeout())
		if matchErr == nil {
			if matches {
				return status, nil
			}
			status.Current = false
			status.Reason = generationMismatchReason
			manifest = nil
		}
		// A concurrent refresher holds the database before it releases the
		// refresh lock. Verification errors therefore proceed through that lock
		// and are decided authoritatively by the second inspection below.
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
		matches, matchErr := databaseGenerationMatches(ctx, status, m.timeout())
		if matchErr != nil {
			return Status{}, matchErr
		}
		if matches {
			return status, nil
		}
		manifest = nil
	}
	provider := m.provider()
	result, err := provider.Refresh(ctx, Request{Repository: repo, State: state, Previous: manifest, Force: force})
	if err != nil {
		return Status{}, fmt.Errorf("refresh with provider %s: %w", provider.ID().Name, err)
	}
	if err := validateResult(result, manifest); err != nil {
		return Status{}, err
	}
	if err := requireUnchangedState(ctx, repo, state, "provider refresh"); err != nil {
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
	newManifest := buildManifest(repo, state, provider.ID(), result.Units, result.Diagnostics)
	if err := db.InvalidateGeneration(ctx); err != nil {
		_ = db.Close()
		return Status{}, err
	}
	// Large compiler universes can contain hundreds of thousands of indexed
	// facts. Bound each storage transaction while the unpublished manifest makes
	// an interrupted refresh observably stale and safely replayable.
	if err := db.ReplaceUnitsIncremental(ctx, result.Batches, result.Removed, 25_000); err != nil {
		_ = db.Close()
		return Status{}, err
	}
	if err := db.SetGeneration(ctx, manifestGeneration(newManifest)); err != nil {
		_ = db.Close()
		return Status{}, err
	}
	if err := db.Close(); err != nil {
		return Status{}, fmt.Errorf("close refreshed database: %w", err)
	}
	if err := requireUnchangedState(ctx, repo, state, "graph publication"); err != nil {
		return Status{}, err
	}
	if err := writeManifest(filepath.Join(repo.StorageDir, "manifest.json"), newManifest); err != nil {
		return Status{}, err
	}
	status = statusFor(repo, state, &newManifest, provider.ID())
	status.Refreshed = true
	return status, nil
}

func databaseGenerationMatches(ctx context.Context, status Status, timeout time.Duration) (bool, error) {
	db, err := storage.Open(ctx, status.DatabasePath, storage.Options{MustExist: true, Timeout: timeout})
	if err != nil {
		return false, err
	}
	generation, generationErr := db.Generation(ctx)
	closeErr := db.Close()
	if generationErr != nil {
		return false, generationErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return generation != "" && generation == status.Generation, nil
}

func requireUnchangedState(ctx context.Context, repo repository.Repository, expected repository.State, phase string) error {
	current, err := repo.Inspect(ctx)
	if err != nil {
		return fmt.Errorf("reinspect repository after %s: %w", phase, err)
	}
	if current.Commit != expected.Commit || current.Tree != expected.Tree || current.Branch != expected.Branch ||
		current.Detached != expected.Detached || overlayDigest(current) != overlayDigest(expected) {
		return fmt.Errorf("repository state changed during %s; refresh must be retried", phase)
	}
	return nil
}

// Inspect reports freshness without changing repository state.
func (m Manager) Inspect(ctx context.Context) (Status, error) {
	_, _, _, status, err := m.inspect(ctx)
	if err != nil || !status.Current {
		return status, err
	}
	matches, generationErr := databaseGenerationMatches(ctx, status, m.timeout())
	if generationErr != nil {
		// Inspect is a read-only status surface: preserve the useful repository
		// status and report unverifiable derived state as non-current. Ensure's
		// locked second check remains responsible for returning persistent errors.
		status.Current = false
		status.Reason = "graph database generation could not be verified: " + generationErr.Error()
		return status, nil
	}
	if !matches {
		status.Current = false
		status.Reason = generationMismatchReason
	}
	return status, nil
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
	status.Diagnostics = append([]string(nil), manifest.Diagnostics...)
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
	status.Generation = manifestGeneration(*manifest)
	return status
}

func manifestGeneration(manifest Manifest) string {
	// UpdatedAt is publication metadata and diagnostics do not change graph
	// facts. All semantic source and provider inputs remain in this projection.
	projection := struct {
		Schema             string
		Complete           bool
		RepositoryIdentity string
		WorktreeID         string
		Provider           ProviderID
		Commit             string
		Tree               string
		Branch             string
		Detached           bool
		OverlayDigest      string
		Units              []Unit
	}{
		manifest.Schema, manifest.Complete, manifest.RepositoryIdentity,
		manifest.WorktreeID, manifest.Provider, manifest.Commit, manifest.Tree,
		manifest.Branch, manifest.Detached, manifest.OverlayDigest, manifest.Units,
	}
	encoded, _ := json.Marshal(projection)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func buildManifest(repo repository.Repository, state repository.State, provider ProviderID, units []Unit, diagnostics []string) Manifest {
	units = append([]Unit(nil), units...)
	diagnostics = append([]string(nil), diagnostics...)
	slices.SortFunc(units, func(a, b Unit) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(diagnostics)
	diagnostics = slices.Compact(diagnostics)
	return Manifest{
		Schema: manifestSchema, Complete: true, RepositoryIdentity: repo.Identity, WorktreeID: repo.WorktreeID,
		Provider: provider, Commit: state.Commit, Tree: state.Tree, Branch: state.Branch, Detached: state.Detached,
		OverlayDigest: overlayDigest(state), Units: units, Diagnostics: diagnostics, UpdatedAt: time.Now().UTC(),
	}
}

func overlayDigest(state repository.State) string {
	encoded, _ := json.Marshal(state.Changes)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateResult(result Result, previous *Manifest) error {
	if len(result.Diagnostics) > 256 {
		return fmt.Errorf("invalid provider result: diagnostics exceed 256 entries")
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic == "" || len(diagnostic) > 8<<10 || strings.IndexByte(diagnostic, 0) >= 0 || !utf8.ValidString(diagnostic) {
			return fmt.Errorf("invalid provider result: malformed diagnostic")
		}
	}
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
		if old, ok := prior[id]; !ok || !reusableUnit(old, unit) {
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

// Owner is assigned by CompositeProvider after a child has decided whether a
// unit is reusable. Ignore an ownership-only change while upgrading a direct
// provider manifest to the composite layout.
func reusableUnit(left, right Unit) bool {
	left.Owner = ""
	right.Owner = ""
	return left == right
}

func readManifest(path string) (*Manifest, error) {
	return readManifestBounded(path, maxManifestBytes)
}

func readManifestBounded(path string, limit int64) (*Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect freshness manifest: %w", err)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("decode freshness manifest: encoded size exceeds %d bytes", limit)
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
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
	return writeManifestBounded(path, manifest, maxManifestBytes)
}

func writeManifestBounded(path string, manifest Manifest, limit int64) error {
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
	bounded := &manifestLimitWriter{writer: temporary, limit: limit}
	encoder := json.NewEncoder(bounded)
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

type manifestLimitWriter struct {
	writer  io.Writer
	limit   int64
	written int64
}

func (writer *manifestLimitWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.limit-writer.written {
		return 0, fmt.Errorf("encoded freshness manifest exceeds %d bytes", writer.limit)
	}
	n, err := writer.writer.Write(value)
	writer.written += int64(n)
	return n, err
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
