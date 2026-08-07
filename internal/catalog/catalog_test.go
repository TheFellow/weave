package catalog_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/catalog"
)

func TestCatalogAddListSyncRemoveAndMissing(t *testing.T) {
	ctx := context.Background()
	root := gitRepository(t, "https://github.com/acme/service.git")
	path := filepath.Join(t.TempDir(), "state", "catalog.db")
	db, err := catalog.Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	added, err := db.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if added.Identity != "github.com/acme/service" || added.WorktreeID != "main" {
		t.Fatalf("added = %#v", added)
	}
	if _, err := db.Add(ctx, root); err != nil {
		t.Fatalf("idempotent Add: %v", err)
	}

	write(t, filepath.Join(root, "new.txt"), "dirty")
	synced, diagnostics, err := db.Sync(ctx, nil)
	if err != nil || len(diagnostics) != 0 || len(synced) != 1 || !synced[0].Dirty {
		t.Fatalf("Sync = %#v, %#v, %v", synced, diagnostics, err)
	}

	missingRoot := gitRepository(t, "https://github.com/acme/missing.git")
	missing, err := db.Add(ctx, missingRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(missingRoot, missingRoot+"-gone"); err != nil {
		t.Fatal(err)
	}
	entries, err := db.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	var missingEntry catalog.Entry
	for _, entry := range entries {
		if entry.Key == missing.Key {
			missingEntry = entry
		}
	}
	if !missingEntry.Missing || missingEntry.Diagnostic == "" {
		t.Fatalf("missing entry = %#v", missingEntry)
	}
	if removed, err := db.Remove(ctx, missing.Key); err != nil || removed != 1 {
		t.Fatalf("Remove = %d, %v", removed, err)
	}
	if removed, err := db.Remove(ctx, added.Identity); err != nil || removed != 1 {
		t.Fatalf("Remove identity = %d, %v", removed, err)
	}
	if _, err := db.Remove(ctx, added.Identity); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("Remove absent error = %v", err)
	}
}

func TestCatalogLinkedWorktreesHaveIndependentEntries(t *testing.T) {
	ctx := context.Background()
	root := gitRepository(t, "https://github.com/acme/worktrees.git")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-b", "feature", linked)
	db, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.Add(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if a.Identity != b.Identity || a.WorktreeID == b.WorktreeID || a.DatabasePath == b.DatabasePath {
		t.Fatalf("main=%#v linked=%#v", a, b)
	}
	entries, err := db.List(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %#v, %v", entries, err)
	}
}

func TestCatalogAddRepairsCanonicalIdentityAtSameRoot(t *testing.T) {
	ctx := context.Background()
	root := gitRepository(t, "https://github.com/acme/before.git")
	db, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "remote", "set-url", "origin", "https://github.com/acme/after.git")
	after, err := db.Add(ctx, root)
	if err != nil {
		t.Fatalf("Add after identity change: %v", err)
	}
	if before.Key == after.Key || after.Identity != "github.com/acme/after" {
		t.Fatalf("before=%#v after=%#v", before, after)
	}
	entries, err := db.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].Key != after.Key || entries[0].Root != before.Root {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
}

func TestCatalogSyncRepairsCanonicalIdentityAtSameRoot(t *testing.T) {
	ctx := context.Background()
	root := gitRepository(t, "https://github.com/acme/sync-before.git")
	db, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "remote", "set-url", "origin", "https://github.com/acme/sync-after.git")
	updates, diagnostics, err := db.Sync(ctx, []string{before.Key})
	if err != nil || len(diagnostics) != 0 || len(updates) != 1 || updates[0].Identity != "github.com/acme/sync-after" {
		t.Fatalf("Sync=%#v diagnostics=%#v error=%v", updates, diagnostics, err)
	}
	entries, err := db.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].Key != updates[0].Key {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
}

func TestCatalogIdentityCollisionDoesNotDisturbEitherRoot(t *testing.T) {
	ctx := context.Background()
	firstRoot := gitRepository(t, "https://github.com/acme/first.git")
	secondRoot := gitRepository(t, "https://github.com/acme/second.git")
	db, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := db.Add(ctx, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.Add(ctx, secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	git(t, firstRoot, "remote", "set-url", "origin", "https://github.com/acme/second.git")
	if _, err := db.Add(ctx, firstRoot); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("collision error = %v", err)
	}
	entries, err := db.List(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	keys := []string{entries[0].Key, entries[1].Key}
	slices.Sort(keys)
	want := []string{first.Key, second.Key}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("keys=%q want=%q", keys, want)
	}
}

func TestCatalogLockContentionIsBounded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	first, err := catalog.Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	started := time.Now()
	_, err = catalog.Open(ctx, path, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "hold the lock") {
		t.Fatalf("Open contention error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("lock wait was not bounded: %v", time.Since(started))
	}
}

func TestDefaultPathRejectsRelativeOverride(t *testing.T) {
	if _, err := catalog.DefaultPath("relative/catalog.db"); !errors.Is(err, catalog.ErrInvalid) {
		t.Fatalf("DefaultPath error = %v", err)
	}
	absolute := filepath.Join(t.TempDir(), "catalog.db")
	if got, err := catalog.DefaultPath(absolute); err != nil || got != absolute {
		t.Fatalf("DefaultPath = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		t.Setenv("WEAVE_CATALOG", "relative")
		if _, err := catalog.DefaultPath(""); !errors.Is(err, catalog.ErrInvalid) {
			t.Fatalf("environment override error = %v", err)
		}
	}
}

func gitRepository(t *testing.T, remote string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test")
	write(t, filepath.Join(root, "README.md"), "fixture")
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-qm", "initial")
	git(t, root, "remote", "add", "origin", remote)
	return root
}

func git(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}
