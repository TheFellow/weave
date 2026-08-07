# Git freshness prior art

This research records the primary sources used for Weave's repository and
freshness lifecycle. The design deliberately asks Git for repository state
instead of interpreting `.git` files or duplicating Git's index logic.

## Repository discovery and paths

[`git rev-parse`](https://git-scm.com/docs/git-rev-parse) is the authoritative
plumbing interface for discovering `--show-toplevel`, `--git-dir`, and
`--git-common-dir`. `--git-path <path>` resolves a path while honoring Git's
path relocation variables and linked-worktree layout. Results may be relative
to the command directory, so a caller must normalize them against that
directory before storing or opening them.

[`gitrepository-layout`](https://git-scm.com/docs/gitrepository-layout)
documents why `.git` cannot be assumed to be a directory: a worktree or
submodule may use a plain text `gitdir:` file. It also distinguishes
worktree-specific administrative files from the common repository directory.

Adopted:

- execute Git with an argument vector and explicit working directory;
- resolve all paths through `rev-parse` and convert them to absolute paths;
- place each worktree's mutable Weave state below `git rev-parse --git-path
  weave`, never by joining `<root>/.git`;
- use the common directory and remote/root history for repository identity,
  while treating checkout paths as locations.

## Linked worktrees, commits, trees, and object identity

[`git worktree`](https://git-scm.com/docs/git-worktree) describes a single
repository with one main worktree and zero or more linked worktrees. Linked
worktrees have private administrative directories under `$GIT_COMMON_DIR/
worktrees`, while refs and objects are generally shared. `git worktree list
--porcelain -z` provides machine-readable worktree inventory, but local
discovery only needs `rev-parse`.

[`git rev-parse`](https://git-scm.com/docs/git-rev-parse) and
[`gitrevisions`](https://git-scm.com/docs/gitrevisions) distinguish a commit
object from its root tree (`HEAD^{tree}`). Two commits can have the same tree,
and a detached HEAD remains a valid commit/tree state. Git object IDs are
content-derived within the repository's selected object format; Weave stores
them as opaque hexadecimal strings rather than assuming SHA-1 length.

Adopted:

- record both commit and tree object IDs;
- record detached/attached HEAD separately from object identity;
- identify mutable overlays by a digest of the resolved worktree-specific Git
  directory, avoiding branch names as identity;
- allow snapshot reuse by exact tree and content fingerprints later.

## Exact dirty-state discovery

[`git status`](https://git-scm.com/docs/git-status) specifies porcelain v2 as a
stable machine-readable format. With `-z`, filenames are NUL terminated,
quoting is disabled, and rename/copy entries carry the destination path before
the origin path. Ordinary entries expose separate index and worktree status;
`?` entries expose untracked paths. `--untracked-files=all` prevents directory
summaries from hiding individual candidate source files.

[`git diff`](https://git-scm.com/docs/git-diff) provides related raw and
name-status `-z` formats for comparing committed inventories. For the live
overlay, porcelain v2 is preferable because one invocation covers staged,
unstaged, renamed, deleted, conflicted, and untracked state.

Adopted:

- parse `git status --porcelain=v2 -z --untracked-files=all` without line or
  shell parsing;
- preserve both sides of rename/copy records;
- hash candidate file contents because Git status identifies change, not the
  semantic input fingerprint expected by providers;
- sort normalized changes before fingerprinting or presenting them.

## Hooks, maintenance, and cache lifecycle

[`githooks`](https://git-scm.com/docs/githooks) documents hooks as files in a
configured hooks directory and notes that a hook without the executable bit is
ignored. Hooks are local configuration, may be bypassed with `--no-verify` for
applicable commands, and not every filesystem/editor change has a Git hook.
Consequently hooks cannot establish query freshness.

[`git maintenance`](https://git-scm.com/docs/git-maintenance) is useful lifecycle
prior art: foreground commands remain correct without maintenance, while
scheduled tasks optimize later work. Weave follows the same separation. Query
time guarantees correctness; optional hooks or a watcher may only warm caches.

Adopted:

- no daemon and no installed hooks are required;
- every real query performs a cheap freshness check;
- defer optional warm hooks and `weave watch` until measured latency warrants
  them;
- provide explicit status and eventual garbage collection for disposable data.

## Content-addressed reuse

[Bazel remote caching](https://bazel.build/remote/caching) documents a useful
split between an action cache and a content-addressable store. Cache entries
are reusable only when their complete input key agrees; file blobs are keyed by
content digest. Go's [`go help cache`](https://pkg.go.dev/cmd/go#hdr-Build_and_test_caching)
similarly treats cached build results as safe, disposable derived state.

Adopted:

- SHA-256 document hashes permit exact reuse independent of path/branch;
- freshness manifests include provider identity/version and repository state,
  not only file modification times;
- provider compilation-unit fingerprints remain the authority for semantic
  reuse and reverse invalidation;
- start with exact local inventories. RIBLT is deferred until an independently
  maintained large inventory makes set reconciliation cheaper than Git/status
  and direct digest comparison.

## Safe cross-platform writer exclusion

Go's [`os.OpenFile`](https://pkg.go.dev/os#OpenFile) specifies `O_CREATE|
O_EXCL` as requiring creation of a new file. [`os.Rename`](https://pkg.go.dev/os#Rename)
is the standard same-filesystem replacement primitive, with documented
platform differences when the destination exists. A lock-file protocol must
therefore avoid assuming Unix advisory locks or deleting a lock solely because
its recorded process ID is unfamiliar.

The standard library's [`os.CreateTemp`](https://pkg.go.dev/os#CreateTemp) and
[`File.Sync`](https://pkg.go.dev/os#File.Sync) supply the safe write pattern for
small manifests: write a sibling temporary file, sync and close it, then rename
it into place. Parent-directory durability is platform-specific and not needed
for a disposable index's correctness contract: an interrupted write is detected
as missing/invalid and rebuilt.

Adopted for the first implementation:

- acquire a repository/worktree writer token with exclusive file creation;
- store bounded diagnostic metadata (PID, host, start time, command);
- wait with context cancellation and a fixed deadline;
- do not automatically break an extant lock based only on age or PID, because
  PID reuse, containers, network filesystems, and host boundaries make that
  unsafe;
- release only the lock instance owned by the caller;
- atomically publish manifests after graph fact transactions complete.

This conservative protocol is portable and diagnosable. A later implementation
may adopt a maintained cross-platform advisory-lock library after testing its
Windows, Unix, process-crash, and network-filesystem semantics.

