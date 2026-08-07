package application

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/storage"
)

func (app Local) snapshotDiff(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	if invocation.Scope == "catalog" {
		return Response{}, fmt.Errorf("%s requires local repository scope", invocation.Command)
	}
	if invocation.DiffBase == "" {
		return Response{}, fmt.Errorf("%s requires a Git baseline", invocation.Command)
	}
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	baseline, err := repo.ResolveRevision(ctx, invocation.DiffBase)
	if err != nil {
		return Response{}, err
	}
	var headRevision *repository.Revision
	if invocation.DiffHead != "" {
		value, resolveErr := repo.ResolveRevision(ctx, invocation.DiffHead)
		if resolveErr != nil {
			return Response{}, resolveErr
		}
		headRevision = &value
	}
	baselineSnapshot, baselineGeneration, baselineDiagnostics, err := app.snapshotAtRevision(ctx, repo, baseline)
	if err != nil {
		return Response{}, fmt.Errorf("index baseline %q: %w", invocation.DiffBase, err)
	}
	baselineDigest, err := graphdiff.Digest(baselineSnapshot)
	if err != nil {
		return Response{}, err
	}

	var headSnapshot graph.Snapshot
	var headIdentity graphdiff.Identity
	var headDiagnostics []string
	if headRevision != nil {
		var generation string
		headSnapshot, generation, headDiagnostics, err = app.snapshotAtRevision(ctx, repo, *headRevision)
		if err != nil {
			return Response{}, fmt.Errorf("index head %q: %w", invocation.DiffHead, err)
		}
		digest, digestErr := graphdiff.Digest(headSnapshot)
		if digestErr != nil {
			return Response{}, digestErr
		}
		headIdentity = revisionIdentity(*headRevision, false, false, generation, digest)
	} else {
		var observed *freshness.Status
		headSnapshot, observed, err = app.currentSnapshot(ctx)
		if err != nil {
			return Response{}, fmt.Errorf("index current worktree: %w", err)
		}
		response.Freshness = observed
		digest, digestErr := graphdiff.Digest(headSnapshot)
		if digestErr != nil {
			return Response{}, digestErr
		}
		headIdentity = graphdiff.Identity{
			Revision: "worktree", Commit: observed.Commit, Tree: observed.Tree,
			Worktree: true, Dirty: observed.Dirty, Generation: observed.Generation, SnapshotDigest: digest,
		}
		headDiagnostics = append(headDiagnostics, observed.Diagnostics...)
	}

	// Capture source inventory only after both semantic snapshots exist. For a
	// live overlay, then reinspect freshness so an edit during Git diff cannot
	// produce a source/graph/identity mixture.
	sourceChanges, err := repo.DiffChanges(ctx, baseline, headRevision)
	if err != nil {
		return Response{}, err
	}
	if headRevision == nil {
		if verifyErr := verifyCurrentDiffSnapshot(ctx, app.Freshness, *response.Freshness); verifyErr != nil {
			return Response{}, verifyErr
		}
	}

	facts, err := graphdiff.Compare(ctx, baselineSnapshot, headSnapshot, invocation.Limit)
	if err != nil {
		return Response{}, err
	}
	result := graphdiff.Result{
		Schema: graphdiff.Schema, Baseline: revisionIdentity(baseline, false, false, baselineGeneration, baselineDigest), Head: headIdentity,
		Diagnostics: prefixedDiagnostics("baseline", baselineDiagnostics, "head", headDiagnostics),
	}
	result.Sources, result.Truncated = boundedSourceChanges(sourceChanges, invocation.Limit)

	switch invocation.Command {
	case "diff graph":
		result.Graph = &facts
		transitions := graphdiff.Transitions(facts)
		result.Transitions = &transitions
		result.Truncated = result.Truncated || facts.Truncated
	case "diff api":
		api := graphdiff.Surfaces(baselineSnapshot, headSnapshot, invocation.Limit)
		result.API = &api
		result.Truncated = result.Truncated || api.Truncated
		if len(api.Surfaces) == 0 {
			result.Diagnostics = append(result.Diagnostics, "no provider-owned public-surface changes; compatibility was not inferred from symbol names")
		}
	case "diff impact", "diff tests":
		impact, diagnostics, impactErr := diffImpact(ctx, headSnapshot, facts, sourceChanges, invocation)
		if impactErr != nil {
			return Response{}, impactErr
		}
		result.Impact = &impact
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		result.Truncated = result.Truncated || facts.Truncated || impact.Truncated
		if invocation.Command == "diff tests" {
			result.Tests = affectedTestEvidence(headSnapshot, impact.Nodes)
		}
	default:
		return Response{}, fmt.Errorf("unsupported snapshot diff command %q", invocation.Command)
	}
	slices.Sort(result.Diagnostics)
	result.Diagnostics = slices.Compact(result.Diagnostics)
	response.Diff = &result
	response.Diagnostics = append(response.Diagnostics, result.Diagnostics...)
	response.Truncated = result.Truncated
	return response, nil
}

func verifyCurrentDiffSnapshot(ctx context.Context, manager *freshness.Manager, observed freshness.Status) error {
	verified, err := manager.Inspect(ctx)
	if err != nil {
		return err
	}
	if !verified.Current || verified.Generation != observed.Generation || verified.Commit != observed.Commit || verified.Tree != observed.Tree || verified.Dirty != observed.Dirty || verified.ChangeCount != observed.ChangeCount {
		return fmt.Errorf("worktree changed during semantic diff; retry the command")
	}
	return nil
}

func (app Local) snapshotAtRevision(ctx context.Context, repo repository.Repository, revision repository.Revision) (graph.Snapshot, string, []string, error) {
	if app.FreshnessFor == nil {
		return graph.Snapshot{}, "", nil, errorsUnavailableHistoricalFacts()
	}
	var snapshot graph.Snapshot
	var generation string
	var diagnostics []string
	err := repo.WithDetachedWorktree(ctx, revision, func(root string) error {
		manager := app.FreshnessFor(root)
		if manager == nil {
			return errorsUnavailableHistoricalFacts()
		}
		status, err := manager.Ensure(ctx, false)
		if err != nil {
			return err
		}
		diagnostics = append(diagnostics, status.Diagnostics...)
		generation = status.Generation
		db, err := storage.Open(ctx, status.DatabasePath, storage.Options{MustExist: true})
		if err != nil {
			return err
		}
		value, exportErr := db.Export(ctx)
		closeErr := db.Close()
		if exportErr != nil {
			return exportErr
		}
		if closeErr != nil {
			return closeErr
		}
		snapshot = value
		return nil
	})
	return snapshot, generation, diagnostics, err
}

func errorsUnavailableHistoricalFacts() error {
	return fmt.Errorf("historical graph facts are unavailable: configure automatic freshness providers")
}

func (app Local) currentSnapshot(ctx context.Context) (graph.Snapshot, *freshness.Status, error) {
	if app.Freshness == nil {
		return graph.Snapshot{}, nil, fmt.Errorf("current worktree facts require automatic freshness management")
	}
	status, err := app.Freshness.Ensure(ctx, false)
	if err != nil {
		return graph.Snapshot{}, nil, err
	}
	db, err := storage.Open(ctx, status.DatabasePath, storage.Options{MustExist: true})
	if err != nil {
		return graph.Snapshot{}, nil, err
	}
	snapshot, exportErr := db.Export(ctx)
	closeErr := db.Close()
	if exportErr != nil {
		return graph.Snapshot{}, nil, exportErr
	}
	if closeErr != nil {
		return graph.Snapshot{}, nil, closeErr
	}
	return snapshot, &status, nil
}

func revisionIdentity(revision repository.Revision, worktree, dirty bool, generation, digest string) graphdiff.Identity {
	return graphdiff.Identity{Revision: revision.Input, Commit: revision.Commit, Tree: revision.Tree, Worktree: worktree, Dirty: dirty, Generation: generation, SnapshotDigest: digest}
}

func prefixedDiagnostics(first string, firstValues []string, second string, secondValues []string) []string {
	result := make([]string, 0, len(firstValues)+len(secondValues))
	for _, value := range firstValues {
		result = append(result, first+": "+value)
	}
	for _, value := range secondValues {
		result = append(result, second+": "+value)
	}
	return result
}

func boundedSourceChanges(changes []repository.DiffChange, limit int) ([]graphdiff.SourceChange, bool) {
	if limit < 1 {
		limit = 100
	}
	truncated := len(changes) > limit
	if len(changes) > limit {
		changes = changes[:limit]
	}
	result := make([]graphdiff.SourceChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, graphdiff.SourceChange{Status: sourceStatus(change.Status), Path: filepath.ToSlash(change.Path), OldPath: filepath.ToSlash(change.OldPath)})
	}
	return result, truncated
}

func sourceStatus(status byte) string {
	switch status {
	case 'A':
		return "added"
	case 'C':
		return "copied"
	case 'D':
		return "deleted"
	case 'M':
		return "modified"
	case 'R':
		return "renamed"
	case 'T':
		return "type-changed"
	case 'U':
		return "unmerged"
	case 'B':
		return "broken"
	default:
		return "unknown"
	}
}

func diffImpact(ctx context.Context, head graph.Snapshot, facts graphdiff.Facts, changes []repository.DiffChange, invocation Invocation) (graphdiff.Impact, []string, error) {
	roots := graphdiff.CurrentRoots(head, facts)
	rootSet := make(map[string]bool, len(roots))
	for _, id := range roots {
		rootSet[id] = true
	}
	removed := removedRoots(facts)
	for _, id := range removed {
		rootSet[id] = true
	}
	paths := make([]string, 0, len(changes))
	pathLimit := invocation.Limit
	if pathLimit < 1 {
		pathLimit = 100
	}
	sourcePathsTruncated := len(changes) > pathLimit
	for _, change := range changes {
		if len(paths) == pathLimit {
			break
		}
		paths = append(paths, change.Path)
	}
	var diagnostics []string
	if len(paths) != 0 {
		fileRoots, values, err := impactRoots(head, paths, nil)
		diagnostics = append(diagnostics, values...)
		if err == nil {
			for _, id := range fileRoots {
				rootSet[id] = true
			}
		} else if len(rootSet) == 0 {
			diagnostics = append(diagnostics, err.Error())
		}
	}
	if sourcePathsTruncated {
		diagnostics = append(diagnostics, "Git source roots truncated by the requested node limit")
	}
	roots = roots[:0]
	for id := range rootSet {
		if id != "" {
			roots = append(roots, id)
		}
	}
	slices.Sort(roots)
	if len(roots) == 0 {
		return graphdiff.Impact{}, diagnostics, nil
	}
	rootsTruncated := len(roots) > pathLimit || sourcePathsTruncated
	if rootsTruncated {
		if len(roots) > pathLimit {
			roots = roots[:pathLimit]
		}
		diagnostics = append(diagnostics, "semantic impact roots truncated by the requested node limit")
	}
	kinds := invocation.Kinds
	if len(kinds) == 0 {
		kinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeDependsOn, graph.EdgeImplements, graph.EdgeImports, graph.EdgeTests, graph.EdgeLinksTo, graph.EdgeEmbeds}
	}
	traversal, err := query.ImpactMany(ctx, newSnapshotImpactStore(head), roots, kinds, bounds(invocation))
	if err != nil {
		return graphdiff.Impact{}, nil, err
	}
	if len(removed) != 0 {
		diagnostics = append(diagnostics, "removed stable IDs were used as reverse-impact roots; disconnected removed facts cannot be traversed in the head graph")
	}
	return graphdiff.Impact{Roots: roots, Nodes: traversal.Nodes, Edges: traversal.Edges, Truncated: traversal.Truncated || rootsTruncated}, diagnostics, nil
}

func removedRoots(facts graphdiff.Facts) []string {
	set := map[string]bool{}
	for _, symbol := range facts.Symbols.Removed {
		set[symbol.ID] = true
	}
	for _, occurrence := range facts.Occurrences.Removed {
		set[occurrence.SymbolID] = true
	}
	for _, edge := range facts.Edges.Removed {
		set[edge.From], set[edge.To] = true, true
	}
	values := make([]string, 0, len(set))
	for id := range set {
		if id != "" {
			values = append(values, id)
		}
	}
	slices.Sort(values)
	return values
}

func affectedTestEvidence(snapshot graph.Snapshot, nodes []string) []graphdiff.AffectedTest {
	impacted := make(map[string]bool, len(nodes))
	for _, id := range nodes {
		impacted[id] = true
	}
	documents := make(map[string]graph.Document, len(snapshot.Documents))
	for _, document := range snapshot.Documents {
		documents[document.ID] = document
	}
	explicit := map[string]graph.Edge{}
	for _, edge := range snapshot.Edges {
		if edge.Kind == graph.EdgeTests && impacted[edge.From] {
			if existing, ok := explicit[edge.From]; !ok || graph.CompareEdges(edge, existing) < 0 {
				explicit[edge.From] = edge
			}
		}
	}
	var result []graphdiff.AffectedTest
	for _, symbol := range snapshot.Symbols {
		if !impacted[symbol.ID] {
			continue
		}
		if edge, ok := explicit[symbol.ID]; ok {
			result = append(result, graphdiff.AffectedTest{Symbol: symbol, Evidence: edge.Evidence, Reason: "explicit tests relationship", EdgeID: edge.ID})
			continue
		}
		if symbol.Kind == "test" {
			result = append(result, graphdiff.AffectedTest{Symbol: symbol, Evidence: symbol.Evidence, Reason: "provider-classified test symbol"})
			continue
		}
		path := filepath.ToSlash(documents[symbol.DocumentID].Path)
		if strings.HasSuffix(path, "_test.go") && (strings.HasPrefix(symbol.DisplayName, "Test") || strings.HasPrefix(symbol.DisplayName, "Benchmark") || strings.HasPrefix(symbol.DisplayName, "Fuzz")) {
			result = append(result, graphdiff.AffectedTest{Symbol: symbol, Evidence: graph.EvidenceSyntactic, Reason: "Go test declaration naming convention"})
		}
	}
	slices.SortFunc(result, func(a, b graphdiff.AffectedTest) int { return strings.Compare(a.Symbol.ID, b.Symbol.ID) })
	return result
}

type snapshotImpactStore struct {
	symbols map[string]graph.Symbol
	from    map[string][]graph.Edge
	to      map[string][]graph.Edge
}

func newSnapshotImpactStore(snapshot graph.Snapshot) *snapshotImpactStore {
	store := &snapshotImpactStore{symbols: make(map[string]graph.Symbol, len(snapshot.Symbols)), from: map[string][]graph.Edge{}, to: map[string][]graph.Edge{}}
	for _, symbol := range snapshot.Symbols {
		store.symbols[symbol.ID] = symbol
	}
	for _, edge := range snapshot.Edges {
		store.from[edge.From] = append(store.from[edge.From], edge)
		store.to[edge.To] = append(store.to[edge.To], edge)
	}
	for key := range store.from {
		slices.SortFunc(store.from[key], graph.CompareEdges)
	}
	for key := range store.to {
		slices.SortFunc(store.to[key], graph.CompareEdges)
	}
	return store
}

func (store *snapshotImpactStore) FindSymbols(_ context.Context, value string, limit int) ([]graph.Symbol, bool, error) {
	if symbol, ok := store.symbols[value]; ok && limit > 0 {
		return []graph.Symbol{symbol}, false, nil
	}
	return nil, false, nil
}

func (store *snapshotImpactStore) Symbol(_ context.Context, id string) (graph.Symbol, bool, error) {
	symbol, ok := store.symbols[id]
	return symbol, ok, nil
}

func (store *snapshotImpactStore) EdgesFrom(ctx context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return boundedSnapshotEdges(ctx, store.from[id], kinds, limit)
}

func (store *snapshotImpactStore) EdgesTo(ctx context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return boundedSnapshotEdges(ctx, store.to[id], kinds, limit)
}

func boundedSnapshotEdges(ctx context.Context, values []graph.Edge, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	wanted := make(map[graph.EdgeKind]bool, len(kinds))
	for _, kind := range kinds {
		wanted[kind] = true
	}
	result := make([]graph.Edge, 0, min(limit, len(values)))
	truncated := false
	for _, edge := range values {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(wanted) != 0 && !wanted[edge.Kind] {
			continue
		}
		if len(result) == limit {
			truncated = true
			continue
		}
		result = append(result, edge)
	}
	return result, truncated, nil
}
