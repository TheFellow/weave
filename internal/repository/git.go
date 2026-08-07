// Package repository discovers Git repositories and describes their exact
// committed snapshot plus live worktree overlay.
package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const maxGitOutput = 16 << 20

// ErrNotRepository indicates that the requested directory is not in a Git
// worktree. Bare repositories are not currently indexable.
var ErrNotRepository = errors.New("not a Git worktree")

// GitError records a failed plumbing invocation without exposing shell text.
type GitError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *GitError) Unwrap() error { return e.Err }

// Repository is one discovered Git worktree and its stable source identity.
type Repository struct {
	Root         string
	GitDir       string
	CommonDir    string
	StorageDir   string
	Identity     string
	WorktreeID   string
	Remote       string
	ObjectFormat string
}

// Change is one porcelain-v2 worktree overlay record.
type Change struct {
	Kind           byte
	IndexStatus    byte
	WorktreeStatus byte
	Path           string
	OriginalPath   string
	ContentHash    string
}

// State identifies the committed snapshot and dirty overlay currently visible
// to a worktree.
type State struct {
	Commit   string
	Tree     string
	Branch   string
	Detached bool
	Changes  []Change
}

// Revision is one fully resolved commit and tree identity. Input is retained
// only for display; comparisons and worktree materialization use the object IDs.
type Revision struct {
	Input  string `json:"revision"`
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

// DiffChange is one Git-authoritative source inventory change.
type DiffChange struct {
	Status  byte
	Path    string
	OldPath string
}

// ResolveRevision resolves one user revision to exact commit and tree objects.
func (r Repository) ResolveRevision(ctx context.Context, value string) (Revision, error) {
	if value == "" || strings.HasPrefix(value, "-") || strings.IndexByte(value, 0) >= 0 {
		return Revision{}, fmt.Errorf("invalid Git revision %q", value)
	}
	runner := gitRunner{directory: r.Root}
	if ambiguous, err := ambiguousRevisionName(ctx, runner, value); err != nil {
		return Revision{}, err
	} else if ambiguous {
		return Revision{}, fmt.Errorf("Git revision %q is ambiguous; use a fully qualified ref", value)
	}
	commit, err := runner.text(ctx, "rev-parse", "--verify", "--end-of-options", value+"^{commit}")
	if err != nil {
		return Revision{}, fmt.Errorf("resolve Git revision %q: %w", value, err)
	}
	tree, err := runner.text(ctx, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return Revision{}, fmt.Errorf("resolve Git tree for %q: %w", value, err)
	}
	return Revision{Input: value, Commit: commit, Tree: tree}, nil
}

func ambiguousRevisionName(ctx context.Context, runner gitRunner, value string) (bool, error) {
	if strings.HasPrefix(value, "refs/") {
		return false, nil
	}
	base := value
	if index := strings.IndexAny(base, "~^:@"); index >= 0 {
		base = base[:index]
	}
	if base == "" || base == "HEAD" {
		return false, nil
	}
	candidates := []string{"refs/heads/" + base, "refs/tags/" + base, "refs/remotes/" + base, "refs/" + base}
	args := []string{"for-each-ref", "--format=%(refname)"}
	args = append(args, candidates...)
	raw, err := runner.run(ctx, args...)
	if err != nil {
		return false, fmt.Errorf("inspect Git revision %q: %w", value, err)
	}
	wanted := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		wanted[candidate] = true
	}
	matches := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if wanted[line] {
			matches++
		}
	}
	return matches > 1, nil
}

// DiffChanges reports source changes between an exact baseline and either an
// exact head revision or the current index/worktree when head is nil.
func (r Repository) DiffChanges(ctx context.Context, baseline Revision, head *Revision) ([]DiffChange, error) {
	if baseline.Commit == "" {
		return nil, errors.New("Git diff baseline commit is empty")
	}
	args := []string{"diff", "--name-status", "-z", "--find-renames", "--no-ext-diff", baseline.Commit}
	if head != nil {
		if head.Commit == "" {
			return nil, errors.New("Git diff head commit is empty")
		}
		args = append(args, head.Commit)
	}
	args = append(args, "--")
	raw, err := (gitRunner{directory: r.Root}).run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve Git source changes: %w", err)
	}
	changes, err := parseNameStatus(raw)
	if err != nil {
		return nil, err
	}
	if head == nil {
		state, inspectErr := r.Inspect(ctx)
		if inspectErr != nil {
			return nil, inspectErr
		}
		present := make(map[string]bool, len(changes))
		for _, change := range changes {
			present[change.Path] = true
		}
		for _, change := range state.Changes {
			if change.Kind == '?' && !present[change.Path] {
				changes = append(changes, DiffChange{Status: 'A', Path: filepath.ToSlash(change.Path)})
			}
		}
	}
	slices.SortFunc(changes, func(a, b DiffChange) int {
		return strings.Compare(a.OldPath+"\x00"+a.Path+"\x00"+string(a.Status), b.OldPath+"\x00"+b.Path+"\x00"+string(b.Status))
	})
	return changes, nil
}

func parseNameStatus(raw []byte) ([]DiffChange, error) {
	fields := bytes.Split(raw, []byte{0})
	var changes []DiffChange
	for index := 0; index < len(fields); {
		if len(fields[index]) == 0 {
			index++
			continue
		}
		status := fields[index]
		index++
		if len(status) == 0 || !strings.ContainsRune("ACDMRTUXB", rune(status[0])) {
			return nil, fmt.Errorf("unrecognized Git name-status record %q", status)
		}
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		if index+paths > len(fields) {
			return nil, errors.New("truncated Git name-status output")
		}
		if paths == 2 {
			changes = append(changes, DiffChange{Status: status[0], OldPath: filepath.ToSlash(string(fields[index])), Path: filepath.ToSlash(string(fields[index+1]))})
		} else {
			changes = append(changes, DiffChange{Status: status[0], Path: filepath.ToSlash(string(fields[index]))})
		}
		index += paths
	}
	return changes, nil
}

// WithDetachedWorktree materializes revision in a temporary linked worktree,
// calls use, and removes both the checkout and Git metadata before returning.
// It never switches or writes the caller's current worktree.
func (r Repository) WithDetachedWorktree(ctx context.Context, revision Revision, use func(string) error) (err error) {
	if revision.Commit == "" {
		return errors.New("temporary worktree commit is empty")
	}
	parent, err := os.MkdirTemp("", "weave-diff-")
	if err != nil {
		return fmt.Errorf("create temporary worktree parent: %w", err)
	}
	target := filepath.Join(parent, "worktree")
	hooks := filepath.Join(parent, "hooks-disabled")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		_ = os.RemoveAll(parent)
		return fmt.Errorf("create empty temporary hooks directory: %w", err)
	}
	registered := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Always try the unique target path. A canceled `worktree add` can
		// register metadata before the checkout directory becomes observable.
		// Removal is harmless when registration never happened, and the original
		// add error remains authoritative in that case.
		_, removeErr := (gitRunner{directory: r.Root}).run(cleanupCtx, "worktree", "remove", "--force", target)
		filesystemErr := os.RemoveAll(parent)
		if registered && removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary Git worktree: %w", removeErr))
		}
		if filesystemErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary worktree files: %w", filesystemErr))
		}
	}()
	if _, err := (gitRunner{directory: r.Root}).run(ctx, "-c", "core.hooksPath="+hooks, "worktree", "add", "--detach", target, revision.Commit); err != nil {
		return fmt.Errorf("materialize Git revision %q: %w", revision.Input, err)
	}
	registered = true
	if err := use(target); err != nil {
		return err
	}
	return nil
}

// DiffPaths returns repository-relative paths changed between revision and the
// current working tree, including current untracked overlay paths.
func (r Repository) DiffPaths(ctx context.Context, revision string) ([]string, error) {
	if revision == "" || strings.HasPrefix(revision, "-") || strings.IndexByte(revision, 0) >= 0 {
		return nil, fmt.Errorf("invalid Git diff revision %q", revision)
	}
	raw, err := (gitRunner{directory: r.Root}).run(ctx, "diff", "--name-only", "-z", "--no-ext-diff", revision, "--")
	if err != nil {
		return nil, fmt.Errorf("resolve Git diff from %q: %w", revision, err)
	}
	var paths []string
	for _, value := range bytes.Split(raw, []byte{0}) {
		if len(value) != 0 {
			paths = append(paths, filepath.ToSlash(string(value)))
		}
	}
	state, err := r.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	for _, change := range state.Changes {
		if change.Kind == '?' {
			paths = append(paths, filepath.ToSlash(change.Path))
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

// Discover asks Git to resolve the repository containing directory.
func Discover(ctx context.Context, directory string) (Repository, error) {
	if directory == "" {
		directory = "."
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve working directory: %w", err)
	}
	runner := gitRunner{directory: directory}
	root, err := runner.text(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	gitDir, err := runner.text(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Repository{}, err
	}
	commonDir, err := runner.text(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, err
	}
	storageDir, err := runner.text(ctx, "rev-parse", "--git-path", "weave")
	if err != nil {
		return Repository{}, err
	}
	root = canonicalPath(absoluteFrom(directory, root))
	gitDir = canonicalPath(absoluteFrom(directory, gitDir))
	commonDir = canonicalPath(absoluteFrom(directory, commonDir))
	storageDir = canonicalPath(absoluteFrom(directory, storageDir))
	format, err := runner.text(ctx, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return Repository{}, err
	}
	remote, identity, err := discoverIdentity(ctx, runner, commonDir, format)
	if err != nil {
		return Repository{}, err
	}
	return Repository{
		Root: filepath.Clean(root), GitDir: filepath.Clean(gitDir), CommonDir: filepath.Clean(commonDir),
		StorageDir: filepath.Clean(storageDir), Identity: identity, WorktreeID: worktreeID(gitDir, commonDir),
		Remote: remote, ObjectFormat: format,
	}, nil
}

// Inspect reads HEAD and the full tracked/untracked worktree overlay.
func (r Repository) Inspect(ctx context.Context) (State, error) {
	runner := gitRunner{directory: r.Root}
	state := State{}
	if commit, err := runner.text(ctx, "rev-parse", "--verify", "HEAD"); err == nil {
		state.Commit = commit
		state.Tree, err = runner.text(ctx, "rev-parse", "--verify", "HEAD^{tree}")
		if err != nil {
			return State{}, err
		}
	} else if !isExitCode(err, 128) {
		return State{}, err
	}
	if branch, err := runner.text(ctx, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		state.Branch = branch
	} else if state.Commit != "" && isExitCode(err, 1) {
		state.Detached = true
	} else if !isExitCode(err, 1) {
		return State{}, err
	}
	raw, err := runner.run(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return State{}, err
	}
	state.Changes, err = parsePorcelainV2(raw)
	if err != nil {
		return State{}, err
	}
	for i := range state.Changes {
		path := filepath.Join(r.Root, filepath.FromSlash(state.Changes[i].Path))
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return State{}, fmt.Errorf("inspect changed path %q: %w", state.Changes[i].Path, statErr)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		digest, readErr := hashRegularFile(path, info)
		if readErr != nil {
			return State{}, fmt.Errorf("hash changed path %q: %w", state.Changes[i].Path, readErr)
		}
		state.Changes[i].ContentHash = digest
	}
	slices.SortFunc(state.Changes, func(a, b Change) int {
		if comparison := strings.Compare(a.Path, b.Path); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.OriginalPath, b.OriginalPath)
	})
	return state, nil
}

func hashRegularFile(name string, expected os.FileInfo) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return "", errors.New("path changed identity before hashing")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func parsePorcelainV2(data []byte) ([]Change, error) {
	fields := bytes.Split(data, []byte{0})
	changes := make([]Change, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		if len(fields[i]) == 0 {
			continue
		}
		record := string(fields[i])
		kind := record[0]
		var values []string
		var change Change
		switch kind {
		case '1':
			values = strings.SplitN(record, " ", 9)
			if len(values) != 9 || len(values[1]) != 2 {
				return nil, fmt.Errorf("invalid ordinary porcelain-v2 record %q", record)
			}
			change = Change{Kind: kind, IndexStatus: values[1][0], WorktreeStatus: values[1][1], Path: values[8]}
		case '2':
			values = strings.SplitN(record, " ", 10)
			if len(values) != 10 || len(values[1]) != 2 || i+1 >= len(fields) || len(fields[i+1]) == 0 {
				return nil, fmt.Errorf("invalid rename/copy porcelain-v2 record %q", record)
			}
			i++
			change = Change{Kind: kind, IndexStatus: values[1][0], WorktreeStatus: values[1][1], Path: values[9], OriginalPath: string(fields[i])}
		case 'u':
			values = strings.SplitN(record, " ", 11)
			if len(values) != 11 || len(values[1]) != 2 {
				return nil, fmt.Errorf("invalid unmerged porcelain-v2 record %q", record)
			}
			change = Change{Kind: kind, IndexStatus: values[1][0], WorktreeStatus: values[1][1], Path: values[10]}
		case '?':
			if len(record) < 3 || record[1] != ' ' {
				return nil, fmt.Errorf("invalid untracked porcelain-v2 record %q", record)
			}
			change = Change{Kind: kind, IndexStatus: '?', WorktreeStatus: '?', Path: record[2:]}
		case '!':
			continue
		default:
			return nil, fmt.Errorf("unsupported porcelain-v2 record %q", record)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func discoverIdentity(ctx context.Context, runner gitRunner, commonDir, objectFormat string) (string, string, error) {
	branch, _ := runner.text(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	var candidates []string
	if value, _ := runner.text(ctx, "config", "--get", "remote.pushDefault"); value != "" {
		candidates = append(candidates, value)
	}
	if branch != "" {
		if value, _ := runner.text(ctx, "config", "--get", "branch."+branch+".remote"); value != "" && value != "." {
			candidates = append(candidates, value)
		}
	}
	candidates = append(candidates, "origin")
	if remotes, err := runner.text(ctx, "remote"); err == nil {
		values := strings.Fields(remotes)
		slices.Sort(values)
		candidates = append(candidates, values...)
	}
	seen := map[string]bool{}
	for _, name := range candidates {
		if seen[name] {
			continue
		}
		seen[name] = true
		value, err := runner.text(ctx, "remote", "get-url", name)
		if err != nil || value == "" {
			continue
		}
		if normalized := normalizeRemote(value); normalized != "" {
			return value, normalized, nil
		}
	}
	if roots, err := runner.text(ctx, "rev-list", "--max-parents=0", "HEAD"); err == nil && roots != "" {
		values := strings.Fields(roots)
		slices.Sort(values)
		return "", "root:" + objectFormat + ":" + values[0], nil
	}
	digest := sha256.Sum256([]byte(filepath.Clean(commonDir)))
	return "", "local:" + hex.EncodeToString(digest[:]), nil
}

func normalizeRemote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Git's SCP-like syntax is not a URL, but it is the common SSH spelling.
	if !strings.Contains(value, "://") {
		colon := strings.IndexByte(value, ':')
		if colon > 0 && !strings.ContainsAny(value[:colon], `/\\`) {
			host := value[:colon]
			if at := strings.LastIndexByte(host, '@'); at >= 0 {
				host = host[at+1:]
			}
			return cleanRemote(strings.ToLower(host), value[colon+1:])
		}
		if absolute, err := filepath.Abs(value); err == nil {
			return "file:" + filepath.ToSlash(filepath.Clean(absolute))
		}
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "file" {
		return "file:" + filepath.ToSlash(filepath.Clean(parsed.Path))
	}
	return cleanRemote(strings.ToLower(parsed.Hostname()), parsed.Path)
}

func cleanRemote(host, path string) string {
	path = strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + path
}

func worktreeID(gitDir, commonDir string) string {
	if samePath(gitDir, commonDir) {
		return "main"
	}
	relative, err := filepath.Rel(commonDir, gitDir)
	if err != nil {
		relative = gitDir
	}
	digest := sha256.Sum256([]byte(filepath.ToSlash(relative)))
	return hex.EncodeToString(digest[:8])
}

func absoluteFrom(base, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(base, value)
}

func samePath(a, b string) bool {
	return canonicalPath(a) == canonicalPath(b)
}

func canonicalPath(value string) string {
	value, _ = filepath.Abs(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return filepath.Clean(value)
}

type gitRunner struct{ directory string }

func (r gitRunner) text(ctx context.Context, args ...string) (string, error) {
	value, err := r.run(ctx, args...)
	return strings.TrimSpace(string(value)), err
}

func (r gitRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	// Repository-local fsmonitor commands are executable code. Weave's
	// read-only discovery and freshness operations must never invoke them.
	commandArgs := append([]string{"-c", "core.fsmonitor=false"}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Dir = r.directory
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxGitOutput, 64<<10
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, &GitError{Args: append([]string(nil), args...), Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("git %s output exceeds %d bytes", strings.Join(args, " "), maxGitOutput)
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.Len()
	if remaining < len(value) {
		b.exceeded = true
		if remaining <= 0 {
			return original, nil
		}
		value = value[:remaining]
	}
	_, _ = b.Buffer.Write(value)
	return original, nil
}

func isExitCode(err error, code int) bool {
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		return false
	}
	var exitErr *exec.ExitError
	return errors.As(gitErr.Err, &exitErr) && exitErr.ExitCode() == code
}
