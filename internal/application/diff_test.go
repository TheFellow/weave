package application

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
)

func TestSnapshotDiffRefToRefDirtyAPIImpactAndTests(t *testing.T) {
	ctx := context.Background()
	root := newDiffRepository(t)
	writeDiffFile(t, root, "model.txt", "one\n")
	diffGit(t, root, "add", ".")
	diffGit(t, root, "commit", "-m", "baseline")
	baseline := diffGit(t, root, "rev-parse", "HEAD")
	writeDiffFile(t, root, "model.txt", "two\n")
	writeDiffFile(t, root, "renamed.md", "old path\n")
	diffGit(t, root, "add", ".")
	diffGit(t, root, "commit", "-m", "head")
	head := diffGit(t, root, "rev-parse", "HEAD")

	managerFor := func(directory string) *freshness.Manager {
		return &freshness.Manager{Directory: directory, Provider: diffFixtureProvider{}, Command: "diff test"}
	}
	app := Local{Freshness: managerFor(root), FreshnessFor: managerFor}
	beforeWorktrees := diffGit(t, root, "worktree", "list", "--porcelain")

	graphResult, err := app.Execute(ctx, Invocation{Command: "diff graph", DiffBase: baseline, DiffHead: head, Scope: "local", Limit: 100, MaxDepth: 8, MaxEdges: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if graphResult.Diff == nil || graphResult.Diff.Schema != graphdiff.Schema || graphResult.Diff.Graph == nil {
		t.Fatalf("graph diff = %#v", graphResult.Diff)
	}
	if graphResult.Diff.Baseline.Commit != baseline || graphResult.Diff.Head.Commit != head || graphResult.Diff.Baseline.SnapshotDigest == "" || graphResult.Diff.Head.SnapshotDigest == "" {
		t.Fatalf("identities = %#v / %#v", graphResult.Diff.Baseline, graphResult.Diff.Head)
	}
	if graphResult.Diff.Baseline.Generation == "" || graphResult.Diff.Head.Generation == "" || graphResult.Diff.Baseline.SnapshotDigest == graphResult.Diff.Head.SnapshotDigest {
		t.Fatalf("snapshot proofs = %#v / %#v", graphResult.Diff.Baseline, graphResult.Diff.Head)
	}
	if len(graphResult.Diff.Sources) != 2 || graphResult.Diff.Sources[0].Path != "model.txt" {
		t.Fatalf("sources = %#v", graphResult.Diff.Sources)
	}
	if len(graphResult.Diff.Graph.Units.Changed) != 1 || len(graphResult.Diff.Graph.Documents.Changed) != 1 || len(graphResult.Diff.Graph.Symbols.Changed) != 1 || len(graphResult.Diff.Graph.Edges.Changed) != 1 {
		t.Fatalf("normalized graph delta = %#v", graphResult.Diff.Graph)
	}
	empty, err := app.Execute(ctx, Invocation{Command: "diff graph", DiffBase: head, DiffHead: head, Scope: "local", Limit: 100, MaxDepth: 8, MaxEdges: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Diff.Sources) != 0 || empty.Diff.Graph.Truncated || len(empty.Diff.Graph.Symbols.Added)+len(empty.Diff.Graph.Symbols.Removed)+len(empty.Diff.Graph.Symbols.Changed) != 0 || empty.Diff.Baseline.SnapshotDigest != empty.Diff.Head.SnapshotDigest {
		t.Fatalf("empty semantic diff = %#v", empty.Diff)
	}

	apiResult, err := app.Execute(ctx, Invocation{Command: "diff api", DiffBase: baseline, DiffHead: head, Scope: "local", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if apiResult.Diff.API == nil || len(apiResult.Diff.API.Surfaces) != 1 || apiResult.Diff.API.Surfaces[0].Compatibility != "unknown" || apiResult.Diff.API.Surfaces[0].Evidence != "provider-surface-fingerprint" {
		t.Fatalf("API = %#v", apiResult.Diff.API)
	}

	testResult, err := app.Execute(ctx, Invocation{Command: "diff tests", DiffBase: baseline, DiffHead: head, Scope: "local", Limit: 100, MaxDepth: 8, MaxEdges: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if testResult.Diff.Impact == nil || !slicesContain(testResult.Diff.Impact.Nodes, "fixture:test") || len(testResult.Diff.Tests) != 1 {
		t.Fatalf("test impact = %#v", testResult.Diff)
	}
	if testResult.Diff.Tests[0].Evidence != graph.EvidenceDeclared || testResult.Diff.Tests[0].EdgeID != "fixture:tests" {
		t.Fatalf("test evidence = %#v", testResult.Diff.Tests)
	}

	writeDiffFile(t, root, "model.txt", "dirty\n")
	writeDiffFile(t, root, "untracked.txt", "overlay\n")
	dirty, err := app.Execute(ctx, Invocation{Command: "diff graph", DiffBase: head, Scope: "local", Limit: 100, MaxDepth: 8, MaxEdges: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Diff.Head.Worktree || !dirty.Diff.Head.Dirty || dirty.Diff.Head.Generation == "" || dirty.Diff.Head.SnapshotDigest == "" {
		t.Fatalf("dirty identity = %#v", dirty.Diff.Head)
	}
	if got := sourcePaths(dirty.Diff.Sources); !reflect.DeepEqual(got, []string{"model.txt", "untracked.txt"}) {
		t.Fatalf("dirty source paths = %v", got)
	}
	if after := diffGit(t, root, "worktree", "list", "--porcelain"); beforeWorktrees != after {
		t.Fatalf("temporary worktree leaked:\nbefore:\n%s\nafter:\n%s", beforeWorktrees, after)
	}
}

func TestSnapshotDiffCleansHistoricalWorktreeOnProviderFailureAndCancellation(t *testing.T) {
	root := newDiffRepository(t)
	writeDiffFile(t, root, "model.txt", "one\n")
	diffGit(t, root, "add", ".")
	diffGit(t, root, "commit", "-m", "baseline")
	baseline := diffGit(t, root, "rev-parse", "HEAD")
	before := diffGit(t, root, "worktree", "list", "--porcelain")

	t.Run("failure", func(t *testing.T) {
		managerFor := func(directory string) *freshness.Manager {
			return &freshness.Manager{Directory: directory, Provider: failingDiffProvider{err: errors.New("fixture failure")}, Command: "diff test"}
		}
		app := Local{Freshness: managerFor(root), FreshnessFor: managerFor}
		if _, err := app.Execute(context.Background(), Invocation{Command: "diff graph", DiffBase: baseline, Scope: "local", Limit: 10}); err == nil || !strings.Contains(err.Error(), "fixture failure") {
			t.Fatalf("failure = %v", err)
		}
		if after := diffGit(t, root, "worktree", "list", "--porcelain"); before != after {
			t.Fatalf("failed provider leaked worktree:\n%s", after)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		provider := failingDiffProvider{started: started}
		managerFor := func(directory string) *freshness.Manager {
			return &freshness.Manager{Directory: directory, Provider: provider, Command: "diff test"}
		}
		app := Local{Freshness: managerFor(root), FreshnessFor: managerFor}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-started
			cancel()
		}()
		if _, err := app.Execute(ctx, Invocation{Command: "diff graph", DiffBase: baseline, Scope: "local", Limit: 10}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		if after := diffGit(t, root, "worktree", "list", "--porcelain"); before != after {
			t.Fatalf("canceled provider leaked worktree:\n%s", after)
		}
	})
}

func TestSnapshotDiffRejectsInvalidAndUnavailableBaselines(t *testing.T) {
	root := newDiffRepository(t)
	writeDiffFile(t, root, "model.txt", "one\n")
	diffGit(t, root, "add", ".")
	diffGit(t, root, "commit", "-m", "baseline")
	app := Local{Directory: root, DatabasePath: filepath.Join(t.TempDir(), "index.db")}
	if _, err := app.Execute(context.Background(), Invocation{Command: "diff graph", DiffBase: "missing", Scope: "local", Limit: 10}); err == nil || !strings.Contains(err.Error(), "resolve Git revision") {
		t.Fatalf("invalid baseline = %v", err)
	}
	if _, err := app.Execute(context.Background(), Invocation{Command: "diff graph", DiffBase: "HEAD", Scope: "local", Limit: 10}); err == nil || !strings.Contains(err.Error(), "historical graph facts are unavailable") {
		t.Fatalf("unavailable baseline = %v", err)
	}
}

func TestVerifyCurrentDiffSnapshotRejectsOverlayMutation(t *testing.T) {
	root := newDiffRepository(t)
	writeDiffFile(t, root, "model.txt", "one\n")
	diffGit(t, root, "add", ".")
	diffGit(t, root, "commit", "-m", "baseline")
	manager := &freshness.Manager{Directory: root, Provider: diffFixtureProvider{}, Command: "diff test"}
	observed, err := manager.Ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	writeDiffFile(t, root, "model.txt", "mutated while diffing\n")
	if err := verifyCurrentDiffSnapshot(context.Background(), manager, observed); err == nil || !strings.Contains(err.Error(), "worktree changed during semantic diff") {
		t.Fatalf("mutation verification = %v", err)
	}
}

type diffFixtureProvider struct{}

func (diffFixtureProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "fixture-diff", Version: "1"}
}

func (diffFixtureProvider) Refresh(_ context.Context, request freshness.Request) (freshness.Result, error) {
	content, err := os.ReadFile(filepath.Join(request.Repository.Root, "model.txt"))
	if err != nil {
		return freshness.Result{}, err
	}
	value := strings.TrimSpace(string(content))
	unit := graph.Unit{ID: "fixture:unit", Provider: "fixture-diff", ProviderVersion: "1", InputFingerprint: value, SurfaceFingerprint: "surface:" + value}
	document := graph.Document{ID: "fixture:document", UnitID: unit.ID, Path: "model.txt", Language: "fixture", ContentHash: value, Provider: unit.Provider, ProviderVersion: unit.ProviderVersion}
	target := graph.Symbol{ID: "fixture:target", UnitID: unit.ID, StableName: "fixture.Target", DisplayName: "Target" + value, Kind: "function", Provider: unit.Provider, Evidence: graph.EvidenceExact}
	test := graph.Symbol{ID: "fixture:test", UnitID: unit.ID, StableName: "fixture.TestTarget", DisplayName: "TestTarget", Kind: "test", Provider: unit.Provider, Evidence: graph.EvidenceExact}
	edge := graph.Edge{ID: "fixture:tests", UnitID: unit.ID, From: test.ID, To: target.ID, Kind: graph.EdgeTests, Provider: unit.Provider, Evidence: graph.EvidenceDeclared}
	if value != "one" {
		edge.Range.Start.Line = 1
		edge.Range.End.Line = 1
	}
	facts := graph.UnitFacts{Unit: unit, Documents: []graph.Document{document}, Symbols: []graph.Symbol{target, test}, Edges: []graph.Edge{edge}}
	return freshness.Result{Batches: []graph.UnitFacts{facts}, Units: []freshness.Unit{{ID: unit.ID, InputFingerprint: value, SurfaceFingerprint: unit.SurfaceFingerprint}}}, nil
}

type failingDiffProvider struct {
	err     error
	started chan struct{}
}

func (failingDiffProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "fixture-failure", Version: "1"}
}

func (provider failingDiffProvider) Refresh(ctx context.Context, _ freshness.Request) (freshness.Result, error) {
	if provider.started != nil {
		close(provider.started)
		<-ctx.Done()
		return freshness.Result{}, ctx.Err()
	}
	return freshness.Result{}, provider.err
}

func newDiffRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	diffGit(t, "", "init", "--initial-branch=main", root)
	diffGit(t, root, "config", "user.email", "weave@example.test")
	diffGit(t, root, "config", "user.name", "Weave Test")
	return root
}

func diffGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeDiffFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func slicesContain(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func sourcePaths(changes []graphdiff.SourceChange) []string {
	values := make([]string, 0, len(changes))
	for _, change := range changes {
		values = append(values, change.Path)
	}
	return values
}
