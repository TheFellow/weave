# ADR 0003: Git repository and freshness lifecycle

- Status: Accepted
- Date: 2026-08-06
- Research: [Git freshness prior art](../prior-art/git-freshness/README.md)

## Context

Weave must transparently remain current across branch switches, detached HEAD,
dirty tracked files, untracked source, renames, deletions, and linked worktrees.
Its database is disposable local state and must never require a daemon, hook,
or committed binary artifact. The graph store already replaces complete
compilation units atomically; repository refresh must preserve that boundary
and must never publish a current marker after partial failure.

## Decision

### Repository discovery and identity

The Git executable is Weave's repository API. Invoke it with argument arrays,
an explicit working directory, bounded output, and context cancellation. Do not
invoke a shell or parse `.git` directly.

Discover the canonical root, worktree-specific Git directory, common Git
directory, and Weave path with `git rev-parse`. Normalize Git's potentially
relative paths against the invocation directory.

Repository identity is, in priority order:

1. a normalized canonical remote URL, preferring `remote.pushDefault`, the
   current branch's remote, then `origin`, then the lexically first remote;
2. `root:<object-format>:<root-commit>` for a repository with history and no
   remote;
3. a deterministic local identity derived from the absolute common Git
   directory for an unborn local repository.

Remote normalization removes transport-only spelling (`https://`, `ssh://`, or
SCP-like `user@host:`), credentials, trailing slash, and terminal `.git`, while
lowercasing the host. Paths remain case-preserving. Filesystem checkout paths
are locations, never the preferred identity.

### Storage and worktree identity

Resolve mutable storage with `git rev-parse --git-path weave`. The main
worktree therefore uses `<common-dir>/weave`; a linked worktree uses its private
`<common-dir>/worktrees/<id>/weave`. This is intentionally per-worktree until
the graph schema can qualify facts by immutable snapshot/build variant.

The database is `index.db`; the published freshness marker is `manifest.json`;
and writer exclusion is `refresh.lock`. A worktree ID is a digest of its
resolved worktree Git directory relative to the common directory. It is not a
branch name, so detached HEAD and branch movement do not change it.

This layout sacrifices cross-worktree graph sharing initially but prevents one
worktree from overwriting another's current graph. A future common
content-addressed store may share immutable document/fact blobs without
changing repository discovery or freshness semantics.

### Snapshot and dirty overlay

Repository state records opaque commit and `HEAD^{tree}` object IDs, attached
branch when present, detached state, and a deterministic dirty overlay. The
overlay comes from `git status --porcelain=v2 -z --untracked-files=all` and
preserves staged/unstaged status, paths, and rename origins.

Each candidate semantic input is hashed by content. The manifest records the
complete provider-owned compilation-unit inventory and fingerprints. File
timestamps are hints at most; equality claims depend on repository/configuration
state and content/provider fingerprints.

### Query-driven freshness

`init`, `index`, and every graph-backed query use one coordinator:

1. discover the repository and inspect current state;
2. load and validate the last complete manifest;
3. compare repository state, provider identity/configuration, and input
   inventory;
4. if current, open the graph and execute immediately;
5. otherwise acquire the bounded worktree writer lock, then inspect again;
6. ask the provider for complete fact batches for affected compilation units;
7. atomically replace each returned complete unit in graph storage;
8. only after every required replacement succeeds, atomically publish the new
   complete manifest;
9. execute the requested query against that state.

The provider interface accepts the prior manifest and exact Git changes as
hints and returns a complete new unit inventory plus complete replacement fact
batches. The core does not infer deletion or delta support from omitted facts.
The first fake provider proves lifecycle semantics; native adapters define real
compilation-unit invalidation later.

An interrupted or failed refresh may leave newly committed unit facts, but the
old manifest remains. The next invocation must refresh again and cannot claim
current. Facts are never partially visible within a unit.

### Locking and publication

One writer per worktree acquires `refresh.lock` with exclusive creation and
writes bounded PID, host, command, and timestamp diagnostics. Contenders wait
with context cancellation up to a configured deadline, then return an error
including the recorded owner. Locks are not automatically stolen based only on
age/PID. Release verifies ownership before removal.

Publish the small JSON manifest using a sibling temporary file, file sync,
close, and rename. Invalid or missing manifests mean stale/rebuild, never
current. The bstore transaction remains the atomic graph fact boundary.

### Hooks and watch

Hooks and `weave watch` are deferred. They may later warm the same coordinator,
but correctness always remains query-driven. No invocation requires an agent,
network service, or background process.

## Consequences

Positive:

- correctness covers linked worktrees and all live Git overlay forms;
- queries cannot silently use a failed refresh as current;
- no shell quoting, daemon lifecycle, committed database, or mandatory hook;
- test providers can exercise exact freshness behavior before language work;
- worktree-local locks avoid unrelated-worktree contention.

Costs:

- each linked worktree initially owns a graph database;
- root-commit and local-path fallbacks are weaker identities than canonical
  remotes and should be surfaced in diagnostics;
- process crashes can leave a lock requiring explicit operator recovery in the
  first version;
- provider refresh is conservative until provider-owned dependency and public
  surface fingerprints support narrow propagation.

## Rejected alternatives

- **Hardcode `.git/weave`:** fails for linked worktrees and relocated Git paths.
- **One common mutable database immediately:** current graph records are not yet
  snapshot-qualified, so worktrees would overwrite each other.
- **Commit the database:** creates binary churn, merge conflicts, and stale
  branch state.
- **Filesystem watcher or hooks as authority:** neither observes all relevant
  changes reliably and both add lifecycle/configuration requirements.
- **Modification-time freshness:** cannot establish content or semantic-context
  equality.
- **Automatically break old locks:** unsafe across PID reuse, hosts,
  containers, suspend, and network filesystems.
- **Use RIBLT for local Git status:** Git already supplies the exact small
  difference more cheaply. Reconsider RIBLT for remote or independent large
  inventories after measurement.
