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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
		content, readErr := os.ReadFile(path)
		if readErr == nil {
			digest := sha256.Sum256(content)
			state.Changes[i].ContentHash = "sha256:" + hex.EncodeToString(digest[:])
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return State{}, fmt.Errorf("hash changed path %q: %w", state.Changes[i].Path, readErr)
		}
	}
	slices.SortFunc(state.Changes, func(a, b Change) int {
		if comparison := strings.Compare(a.Path, b.Path); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.OriginalPath, b.OriginalPath)
	})
	return state, nil
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
	command := exec.CommandContext(ctx, "git", args...)
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
