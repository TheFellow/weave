package application

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
)

func TestResidentReusesOneDatabaseHandleAndReleasesIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(ctx, workspaceFixture()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	resident := (Local{DatabasePath: path}).Resident()
	first, err := resident.Execute(ctx, Invocation{Command: "symbols", Arguments: []string{"README"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Symbols) == 0 {
		t.Fatalf("first query = %#v", first)
	}
	handle := resident.db
	second, err := resident.Execute(ctx, Invocation{Command: "workspace outline", Arguments: []string{"README.md"}, Limit: 10, MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if resident.db != handle || len(second.Symbols) < 2 {
		t.Fatalf("resident did not reuse the database handle: response=%#v", second)
	}
	if _, err := storage.Open(ctx, path, storage.Options{MustExist: true, Timeout: time.Millisecond}); err == nil {
		t.Fatal("second database open succeeded while resident owned the file")
	}
	if err := resident.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, path, storage.Options{MustExist: true, Timeout: time.Second})
	if err != nil {
		t.Fatalf("open after resident close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResidentReleasesDatabaseBeforeOneShotOperation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(ctx, workspaceFixture()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	resident := (Local{DatabasePath: path}).Resident()
	if _, err := resident.Execute(ctx, Invocation{Command: "symbols", Arguments: []string{"README"}, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if resident.db == nil {
		t.Fatal("query did not open resident database")
	}
	if _, err := resident.Execute(ctx, Invocation{Command: "verify"}); err != nil {
		t.Fatal(err)
	}
	if resident.db != nil {
		t.Fatal("one-shot operation retained resident database")
	}
}

func TestApplicationSearchesTermsWithoutSpendingResponseTokensOnThem(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	facts := workspaceFixture()
	facts.Symbols[0].SearchTerms = []string{"artifacts", "latency", "retained"}
	db, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	app := Local{DatabasePath: path}
	result, err := app.Execute(ctx, Invocation{Command: "symbols", Arguments: []string{"artifacts"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 1 || len(result.Symbols[0].SearchTerms) != 0 {
		t.Fatalf("symbol result leaked discovery terms: %#v", result.Symbols)
	}
	exported, err := app.Execute(ctx, Invocation{Command: "export"})
	if err != nil {
		t.Fatal(err)
	}
	foundTerms := false
	if exported.Export != nil {
		for _, symbol := range exported.Export.Symbols {
			foundTerms = foundTerms || len(symbol.SearchTerms) != 0
		}
	}
	if !foundTerms {
		t.Fatalf("diagnostic export lost discovery terms: %#v", exported.Export)
	}
}

type residentProvider struct{ calls int }

func (*residentProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "resident-fixture", Version: "1"}
}

func (provider *residentProvider) Refresh(context.Context, freshness.Request) (freshness.Result, error) {
	provider.calls++
	facts := workspaceFixture()
	return freshness.Result{
		Batches: []graph.UnitFacts{facts},
		Units:   []freshness.Unit{{ID: facts.Unit.ID, InventoryDigest: "resident-fixture-v1"}},
	}, nil
}

func TestResidentObserverRefreshesChangedSourceBeforeNextQuery(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	for _, command := range [][]string{
		{"init", "--initial-branch=main", root},
		{"-C", root, "config", "user.email", "weave@example.test"},
		{"-C", root, "config", "user.name", "Weave Test"},
	} {
		if output, err := exec.Command("git", command...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", command, err, output)
		}
	}
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"-C", root, "add", "."}, {"-C", root, "commit", "-m", "initial"}} {
		if output, err := exec.Command("git", command...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", command, err, output)
		}
	}
	provider := &residentProvider{}
	manager := &freshness.Manager{Directory: root, Provider: provider, Command: "weave"}
	resident := (Local{Freshness: manager}).Resident()
	defer resident.Close()
	if _, err := resident.Execute(ctx, Invocation{Command: "symbols", Arguments: []string{"README"}, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	firstHandle := resident.db
	if provider.calls != 1 {
		t.Fatalf("initial refresh calls = %d, want 1", provider.calls)
	}
	if err := os.WriteFile(source, []byte("package fixture\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resident.mu.Lock()
		pending := resident.pending != nil
		resident.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resident observer did not detect changed source")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := resident.Execute(ctx, Invocation{Command: "symbols", Arguments: []string{"README"}, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || resident.db == firstHandle {
		t.Fatalf("changed source did not refresh/reopen: calls=%d same_handle=%v", provider.calls, resident.db == firstHandle)
	}
}
