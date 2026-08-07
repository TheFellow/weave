package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndInspectRepositoryStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newRepository(t)
	writeFile(t, root, "main.go", "package main\n")
	writeFile(t, root, "delete.go", "package main\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	git(t, root, "remote", "add", "origin", "git@GitHub.COM:TheFellow/weave.git")

	repo, err := Discover(ctx, filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalPath(root)
	if repo.Root != root || repo.GitDir != filepath.Join(root, ".git") || repo.CommonDir != repo.GitDir {
		t.Fatalf("Discover() paths = %#v", repo)
	}
	if repo.StorageDir != filepath.Join(root, ".git", "weave") || repo.WorktreeID != "main" {
		t.Fatalf("Discover() storage = %#v", repo)
	}
	if repo.Identity != "github.com/TheFellow/weave" || repo.ObjectFormat == "" {
		t.Fatalf("Discover() identity = %#v", repo)
	}
	initial, err := repo.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Commit == "" || initial.Tree == "" || initial.Branch == "" || initial.Detached || len(initial.Changes) != 0 {
		t.Fatalf("initial state = %#v", initial)
	}

	git(t, root, "switch", "-c", "other")
	writeFile(t, root, "branch.go", "package branch\n")
	git(t, root, "add", "branch.go")
	git(t, root, "commit", "-m", "branch")
	other, err := repo.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if other.Commit == initial.Commit || other.Tree == initial.Tree || other.Branch != "other" {
		t.Fatalf("branch state = %#v, initial %#v", other, initial)
	}

	writeFile(t, root, "main.go", "package main\n// dirty\n")
	writeFile(t, root, "untracked name.go", "package extra\n")
	git(t, root, "mv", "branch.go", "renamed.go")
	git(t, root, "rm", "delete.go")
	dirty, err := repo.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, dirty.Changes, "main.go", "", '1', '.', 'M', true)
	assertChange(t, dirty.Changes, "untracked name.go", "", '?', '?', '?', true)
	assertChange(t, dirty.Changes, "renamed.go", "branch.go", '2', 'R', '.', true)
	assertChange(t, dirty.Changes, "delete.go", "", '1', 'D', '.', false)

	git(t, root, "reset", "--hard", "HEAD")
	if err := os.Remove(filepath.Join(root, "untracked name.go")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "checkout", "--detach", "HEAD")
	detached, err := repo.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !detached.Detached || detached.Branch != "" || detached.Commit != other.Commit {
		t.Fatalf("detached state = %#v", detached)
	}
}

func TestLinkedWorktreeUsesPrivateGitPathAndSharedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newRepository(t)
	writeFile(t, root, "main.go", "package main\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-b", "linked", linked)

	main, err := Discover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := Discover(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if worktree.CommonDir != main.CommonDir || worktree.Identity != main.Identity {
		t.Fatalf("linked/common identity differs: main=%#v linked=%#v", main, worktree)
	}
	if worktree.GitDir == worktree.CommonDir || worktree.WorktreeID == "main" {
		t.Fatalf("linked worktree identity = %#v", worktree)
	}
	if !strings.HasPrefix(worktree.StorageDir, worktree.GitDir+string(filepath.Separator)) {
		t.Fatalf("storage %q is not under worktree git dir %q", worktree.StorageDir, worktree.GitDir)
	}
}

func TestDiffPathsIncludesWorkingTreeAndUntrackedChanges(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeFile(t, root, "main.go", "package main\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	base := git(t, root, "rev-parse", "HEAD")
	writeFile(t, root, "main.go", "package changed\n")
	writeFile(t, root, "untracked name.go", "package changed\n")
	repo, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := repo.DiffPaths(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths, ","); got != "main.go,untracked name.go" {
		t.Fatalf("DiffPaths = %q", got)
	}
	if _, err := repo.DiffPaths(context.Background(), "--output=/tmp/nope"); err == nil {
		t.Fatal("option-like revision accepted")
	}
}

func TestLocalIdentityFallsBackToRootCommitThenLocalDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newRepository(t)
	before, err := Discover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(filepath.Join(root, ".git")))
	if want := "local:" + hex.EncodeToString(digest[:]); before.Identity != want {
		t.Fatalf("unborn identity = %q, want %q", before.Identity, want)
	}
	writeFile(t, root, "main.go", "package main\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	after, err := Discover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(after.Identity, "root:"+after.ObjectFormat+":") {
		t.Fatalf("root identity = %q", after.Identity)
	}
}

func TestDiscoverOutsideRepository(t *testing.T) {
	t.Parallel()
	_, err := Discover(context.Background(), t.TempDir())
	if !errorsIs(err, ErrNotRepository) {
		t.Fatalf("Discover() error = %v, want ErrNotRepository", err)
	}
}

func assertChange(t *testing.T, changes []Change, path, original string, kind, index, worktree byte, hasHash bool) {
	t.Helper()
	for _, change := range changes {
		if change.Path != path {
			continue
		}
		if change.OriginalPath != original || change.Kind != kind || change.IndexStatus != index || change.WorktreeStatus != worktree {
			t.Fatalf("change %q = %#v", path, change)
		}
		if (change.ContentHash != "") != hasHash {
			t.Fatalf("change %q hash = %q, want present %v", path, change.ContentHash, hasHash)
		}
		return
	}
	t.Fatalf("change %q missing from %#v", path, changes)
}

func newRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	git(t, "", "init", "--initial-branch=main", root)
	git(t, root, "config", "user.email", "weave@example.test")
	git(t, root, "config", "user.name", "Weave Test")
	return canonicalPath(root)
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		value, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = value.Unwrap()
	}
	return false
}
