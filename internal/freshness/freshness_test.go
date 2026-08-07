package freshness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/storage"
)

func TestEnsureRefreshesOnlyReturnedUnitsAndPublishesCompleteState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := freshRepository(t)
	provider := &fakeProvider{id: ProviderID{Name: "fixture", Version: "1"}}
	provider.results = []Result{{
		Batches:     []graph.UnitFacts{facts("a", "Alpha"), facts("b", "Beta")},
		Units:       []Unit{{ID: "b", InventoryDigest: "b1"}, {ID: "a", InventoryDigest: "a1"}},
		Diagnostics: []string{"fixture warning"},
	}, {
		Batches: []graph.UnitFacts{facts("a", "AlphaChanged")},
		Units:   []Unit{{ID: "a", InventoryDigest: "a2"}, {ID: "b", InventoryDigest: "b1"}},
	}}
	manager := Manager{Directory: root, Provider: provider, Command: "test"}

	first, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Current || !first.Refreshed || provider.Calls() != 1 || !slices.Equal(first.Diagnostics, []string{"fixture warning"}) {
		t.Fatalf("first Ensure() = %#v, calls %d", first, provider.Calls())
	}
	if !strings.HasPrefix(first.Generation, "sha256:") {
		t.Fatalf("first generation = %q", first.Generation)
	}
	manifest, err := readManifest(first.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Complete || manifest.Units[0].ID != "a" || manifest.Units[1].ID != "b" {
		t.Fatalf("manifest = %#v", manifest)
	}

	second, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Current || second.Refreshed || provider.Calls() != 1 || !slices.Equal(second.Diagnostics, []string{"fixture warning"}) {
		t.Fatalf("unchanged Ensure() = %#v, calls %d", second, provider.Calls())
	}
	if second.Generation != first.Generation {
		t.Fatalf("unchanged generation %q != %q", second.Generation, first.Generation)
	}

	writeFreshFile(t, root, "untracked.go", "package fixture\n")
	third, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Current || !third.Refreshed || !third.Dirty || provider.Calls() != 2 {
		t.Fatalf("dirty Ensure() = %#v, calls %d", third, provider.Calls())
	}
	if third.Generation == first.Generation {
		t.Fatalf("changed worktree retained generation %q", third.Generation)
	}
	requests := provider.Requests()
	if requests[1].Previous == nil || len(requests[1].Previous.Units) != 2 || len(requests[1].State.Changes) != 1 || requests[1].State.Changes[0].ContentHash == "" {
		t.Fatalf("incremental request = %#v", requests[1])
	}
	db, err := storage.Open(ctx, third.DatabasePath, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got := symbolNames(snapshot.Symbols); got != "AlphaChanged,Beta" {
		t.Fatalf("symbols after narrow replacement = %q", got)
	}
}

func TestObserveIdentifiesExactRepositoryStateWithoutChangingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := freshRepository(t)
	manager := Manager{Directory: root, Provider: EmptyProvider{}, Command: "observe test"}
	before, err := manager.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Token == "" || before.Status.Current {
		t.Fatalf("initial observation = %#v", before)
	}
	if _, err := manager.Ensure(ctx, false); err != nil {
		t.Fatal(err)
	}
	current, err := manager.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Token != before.Token || !current.Status.Current {
		t.Fatalf("current observation = %#v, before %#v", current, before)
	}
	freshGit(t, root, "switch", "-c", "other")
	branch, err := manager.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if branch.Token == current.Token || branch.Status.Current {
		t.Fatalf("branch observation = %#v, current %#v", branch, current)
	}
	freshGit(t, root, "switch", "main")
	writeFreshFile(t, root, "main.go", "package changed\n")
	changed, err := manager.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Token == "" || changed.Token == current.Token || changed.Status.Current {
		t.Fatalf("changed observation = %#v, current %#v", changed, current)
	}

	missing, err := (Manager{Directory: filepath.Join(t.TempDir(), "not-a-repository")}).Observe(ctx)
	if err == nil || missing.Token != "" {
		t.Fatalf("failed repository observation = %#v, %v", missing, err)
	}
}

func TestFailedRefreshNeverPublishesCurrentManifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := freshRepository(t)
	provider := &fakeProvider{id: ProviderID{Name: "fixture", Version: "1"}, results: []Result{{
		Batches: []graph.UnitFacts{facts("a", "Alpha")}, Units: []Unit{{ID: "a", InventoryDigest: "a1"}},
	}}}
	manager := Manager{Directory: root, Provider: provider}
	first, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFreshFile(t, root, "main.go", "package changed\n")
	provider.err = errors.New("provider interrupted")
	if _, err := manager.Ensure(ctx, false); err == nil || !strings.Contains(err.Error(), "provider interrupted") {
		t.Fatalf("Ensure() error = %v", err)
	}
	after, err := os.ReadFile(first.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed refresh changed complete manifest")
	}
	status, err := manager.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Current || status.Reason != "repository state changed" {
		t.Fatalf("status after failed refresh = %#v", status)
	}
}

func TestGenerationMismatchForcesCompleteReplay(t *testing.T) {
	ctx := context.Background()
	root := freshRepository(t)
	provider := &fakeProvider{id: ProviderID{Name: "fixture", Version: "1"}, results: []Result{
		{Batches: []graph.UnitFacts{facts("a", "Alpha")}, Units: []Unit{{ID: "a", InventoryDigest: "a1"}}},
		{Batches: []graph.UnitFacts{facts("a", "Alpha")}, Units: []Unit{{ID: "a", InventoryDigest: "a1"}}},
	}}
	manager := Manager{Directory: root, Provider: provider, Command: "test"}
	first, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(ctx, first.DatabasePath, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InvalidateGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	db.Close()
	observed, err := manager.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.Status.Current || observed.Token == "" {
		t.Fatalf("cheap source/manifest observation = %#v", observed)
	}
	inspected, err := manager.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Current || inspected.Reason != generationMismatchReason {
		t.Fatalf("Inspect after generation invalidation = %#v", inspected)
	}
	second, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Current || !second.Refreshed || provider.Calls() != 2 || second.Generation != first.Generation {
		t.Fatalf("replayed status=%#v first=%#v calls=%d", second, first, provider.Calls())
	}
	requests := provider.Requests()
	if requests[1].Previous != nil {
		t.Fatalf("generation mismatch reused previous manifest: %#v", requests[1].Previous)
	}
}

func TestEnsureWaitsForConcurrentRefreshAfterTransientGenerationVerification(t *testing.T) {
	ctx := context.Background()
	root := freshRepository(t)
	provider := &fakeProvider{id: ProviderID{Name: "fixture", Version: "1"}, results: []Result{{
		Batches: []graph.UnitFacts{facts("a", "Alpha")}, Units: []Unit{{ID: "a", InventoryDigest: "a1"}},
	}}}
	manager := Manager{Directory: root, Provider: provider, LockTimeout: 250 * time.Millisecond, Command: "contender"}
	first, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := repositoryDiscover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	heldLock, err := acquire(ctx, filepath.Join(repo.StorageDir, "refresh.lock"), time.Second, "holder")
	if err != nil {
		t.Fatal(err)
	}
	heldDB, err := storage.Open(ctx, first.DatabasePath, storage.Options{MustExist: true})
	if err != nil {
		heldLock.release()
		t.Fatal(err)
	}
	inspected, err := manager.Inspect(ctx)
	if err != nil || inspected.Current || !strings.Contains(inspected.Reason, "generation could not be verified") {
		heldDB.Close()
		heldLock.release()
		t.Fatalf("Inspect during refresh = %#v, %v", inspected, err)
	}

	type ensureResult struct {
		status Status
		err    error
	}
	result := make(chan ensureResult, 1)
	go func() {
		status, err := manager.Ensure(ctx, false)
		result <- ensureResult{status, err}
	}()
	// The first generation check times out at 250 ms. Release while Ensure is
	// waiting on the authoritative refresh lock so its second check can succeed.
	time.Sleep(350 * time.Millisecond)
	if err := heldDB.Close(); err != nil {
		heldLock.release()
		t.Fatal(err)
	}
	heldLock.release()
	select {
	case got := <-result:
		if got.err != nil || !got.status.Current || got.status.Refreshed || got.status.Generation != first.Generation || provider.Calls() != 1 {
			t.Fatalf("Ensure after concurrent refresh = %#v, %v; calls=%d", got.status, got.err, provider.Calls())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ensure did not complete after concurrent refresh released its locks")
	}
}

func TestEnsureDoesNotPublishFactsFromAChangingWorktree(t *testing.T) {
	ctx := context.Background()
	root := freshRepository(t)
	provider := &fakeProvider{
		id: ProviderID{Name: "fixture", Version: "1"},
		results: []Result{
			{Batches: []graph.UnitFacts{facts("a", "Alpha")}, Units: []Unit{{ID: "a"}}},
			{Batches: []graph.UnitFacts{facts("a", "Alpha")}, Units: []Unit{{ID: "a"}}},
		},
		mutate: func(Request) { writeFreshFile(t, root, "main.go", "package changed\n") },
	}
	manager := Manager{Directory: root, Provider: provider}
	if _, err := manager.Ensure(ctx, false); err == nil || !strings.Contains(err.Error(), "state changed during provider refresh") {
		t.Fatalf("changing refresh error = %v", err)
	}
	repo, err := repository.Discover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.StorageDir, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changing refresh published a manifest: %v", err)
	}
	status, err := manager.Ensure(ctx, false)
	if err != nil || !status.Current {
		t.Fatalf("retry status=%#v error=%v", status, err)
	}
}

func TestManifestReadAndWriteShareEncodedBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	manifest := Manifest{Schema: manifestSchema, Complete: true, Diagnostics: []string{strings.Repeat("\x01", 64)}}
	if err := writeManifestBounded(path, manifest, 128); err == nil || !strings.Contains(err.Error(), "encoded freshness manifest exceeds") {
		t.Fatalf("bounded write error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized manifest was published: %v", err)
	}
	if err := writeManifestBounded(path, manifest, 4096); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifestBounded(path, 128); err == nil || !strings.Contains(err.Error(), "encoded size exceeds") {
		t.Fatalf("bounded read error = %v", err)
	}
	decoded, err := readManifestBounded(path, 4096)
	if err != nil || !slices.Equal(decoded.Diagnostics, manifest.Diagnostics) {
		t.Fatalf("decoded manifest=%#v error=%v", decoded, err)
	}
}

func TestInvalidBatchRollsBackAllReturnedUnits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := freshRepository(t)
	provider := &fakeProvider{id: ProviderID{Name: "fixture", Version: "1"}, results: []Result{{
		Batches: []graph.UnitFacts{facts("a", "OldA"), facts("b", "OldB")}, Units: []Unit{{ID: "a"}, {ID: "b"}},
	}}}
	manager := Manager{Directory: root, Provider: provider}
	status, err := manager.Ensure(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	writeFreshFile(t, root, "main.go", "package changed\n")
	invalid := facts("b", "NewB")
	invalid.Symbols[0].ID = "a:symbol" // Cross-batch conflict occurs inside the storage transaction.
	provider.results = append(provider.results, Result{
		Batches: []graph.UnitFacts{facts("a", "NewA"), invalid}, Units: []Unit{{ID: "a"}, {ID: "b"}},
	})
	if _, err := manager.Ensure(ctx, false); err == nil {
		t.Fatal("Ensure(invalid batch) error = nil")
	}
	db, err := storage.Open(ctx, status.DatabasePath, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got := symbolNames(snapshot.Symbols); got != "OldA,OldB" {
		t.Fatalf("symbols after rollback = %q", got)
	}
}

func TestConcurrentWriterTimesOutWithOwnerDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := freshRepository(t)
	manager := Manager{Directory: root, Provider: &fakeProvider{id: ProviderID{Name: "fixture", Version: "1"}}, LockTimeout: 60 * time.Millisecond, Command: "contender"}
	repo, err := repositoryDiscover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	held, err := acquire(ctx, filepath.Join(repo.StorageDir, "refresh.lock"), time.Second, "holder")
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()
	_, err = manager.Ensure(ctx, false)
	var timeout *LockTimeoutError
	if !errors.As(err, &timeout) || !strings.Contains(timeout.Owner, "holder") || !strings.Contains(timeout.Owner, "pid=") {
		t.Fatalf("Ensure() error = %#v", err)
	}
}

// This variable keeps repository discovery replaceable only inside this test
// package without exporting freshness internals.
var repositoryDiscover = func(ctx context.Context, root string) (repository.Repository, error) {
	return repository.Discover(ctx, root)
}

type fakeProvider struct {
	mu       sync.Mutex
	id       ProviderID
	results  []Result
	requests []Request
	err      error
	mutate   func(Request)
}

func (p *fakeProvider) ID() ProviderID { return p.id }
func (p *fakeProvider) Refresh(_ context.Context, request Request) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	if p.mutate != nil {
		mutate := p.mutate
		p.mutate = nil
		mutate(request)
	}
	if p.err != nil {
		return Result{}, p.err
	}
	if len(p.results) < len(p.requests) {
		return Result{}, errors.New("unexpected provider refresh")
	}
	return p.results[len(p.requests)-1], nil
}
func (p *fakeProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}
func (p *fakeProvider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

func facts(unit, name string) graph.UnitFacts {
	document := unit + ":main.go"
	return graph.UnitFacts{
		Unit:      graph.Unit{ID: unit, Provider: "fixture", ProviderVersion: "1", Language: "go", InventoryDigest: name},
		Documents: []graph.Document{{ID: document, UnitID: unit, Path: unit + ".go", Language: "go", ContentHash: "sha256:" + unit, Provider: "fixture", ProviderVersion: "1"}},
		Symbols:   []graph.Symbol{{ID: unit + ":symbol", UnitID: unit, StableName: "fixture." + name, DisplayName: name, Kind: "function", DocumentID: document, Provider: "fixture", Evidence: graph.EvidenceExact}},
	}
}

func symbolNames(symbols []graph.Symbol) string {
	names := make([]string, len(symbols))
	for i, symbol := range symbols {
		names[i] = symbol.DisplayName
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func freshRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	freshGit(t, "", "init", "--initial-branch=main", root)
	freshGit(t, root, "config", "user.email", "weave@example.test")
	freshGit(t, root, "config", "user.name", "Weave Test")
	writeFreshFile(t, root, "main.go", "package fixture\n")
	freshGit(t, root, "add", ".")
	freshGit(t, root, "commit", "-m", "initial")
	return root
}

func freshGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeFreshFile(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
