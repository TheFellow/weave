// Package graphdiff compares two normalized graph snapshots without interpreting
// provider-specific language semantics.
package graphdiff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
)

// Schema is the machine-readable snapshot delta contract shared by the CLI and
// the human explorer.
const Schema = "weave.snapshot-diff/v1"

// Identity names one exact side of a comparison.
type Identity struct {
	Revision string `json:"revision"`
	Commit   string `json:"commit"`
	Tree     string `json:"tree"`
	Worktree bool   `json:"worktree,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	// Generation proves the exact freshness observation. Historical temporary
	// worktree generations are intentionally ephemeral because WorktreeID is
	// part of that digest; SnapshotDigest is the stable semantic identity.
	Generation     string `json:"generation,omitempty"`
	SnapshotDigest string `json:"snapshot_digest"`
}

// SourceChange is one Git-authoritative source inventory change. Status uses
// Git's stable name-status vocabulary: added, copied, deleted, modified,
// renamed, type-changed, or unmerged.
type SourceChange struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
}

// Change retains both normalized values for one stable identity.
type Change[T any] struct {
	Before T `json:"before"`
	After  T `json:"after"`
}

// Delta is one stable-ID fact collection.
type Delta[T any] struct {
	Added   []T         `json:"added,omitempty"`
	Removed []T         `json:"removed,omitempty"`
	Changed []Change[T] `json:"changed,omitempty"`
}

// Facts is the complete normalized graph delta. Source changes intentionally
// live outside this structure because a file edit and a semantic edit are not
// equivalent claims.
type Facts struct {
	Units       Delta[graph.Unit]       `json:"units"`
	Documents   Delta[graph.Document]   `json:"documents"`
	Symbols     Delta[graph.Symbol]     `json:"symbols"`
	Occurrences Delta[graph.Occurrence] `json:"occurrences"`
	Edges       Delta[graph.Edge]       `json:"edges"`
	Truncated   bool                    `json:"truncated"`
}

// APISurface is an honest provider-owned public-surface change. Compatibility
// remains unknown because opaque cross-language fingerprints cannot establish
// source or binary breakage.
type APISurface struct {
	UnitID        string `json:"unit_id"`
	Provider      string `json:"provider"`
	Change        string `json:"change"`
	Before        string `json:"before,omitempty"`
	After         string `json:"after,omitempty"`
	Evidence      string `json:"evidence"`
	Compatibility string `json:"compatibility"`
}

// API is the bounded provider-surface projection.
type API struct {
	Surfaces  []APISurface `json:"surfaces"`
	Truncated bool         `json:"truncated"`
}

// Impact is a reverse traversal rooted in current changed graph facts.
type Impact struct {
	Roots     []string     `json:"roots"`
	Nodes     []string     `json:"nodes"`
	Edges     []graph.Edge `json:"edges"`
	Truncated bool         `json:"truncated"`
}

// AffectedTest explains why a current test symbol was selected.
type AffectedTest struct {
	Symbol   graph.Symbol   `json:"symbol"`
	Evidence graph.Evidence `json:"evidence"`
	Reason   string         `json:"reason"`
	EdgeID   string         `json:"edge_id,omitempty"`
}

// Transition is one stable graph key and the animation operation consumers
// should apply. IDs are normalized semantic IDs, never array positions.
type Transition struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// TransitionSet is the compact projection needed by keyed graph renderers.
type TransitionSet struct {
	Nodes []Transition `json:"nodes,omitempty"`
	Edges []Transition `json:"edges,omitempty"`
}

// Result is the versioned semantic diff delivered by application surfaces.
// Command-specific callers may omit projections they did not request.
type Result struct {
	Schema      string         `json:"schema"`
	Baseline    Identity       `json:"baseline"`
	Head        Identity       `json:"head"`
	Sources     []SourceChange `json:"sources,omitempty"`
	Graph       *Facts         `json:"graph,omitempty"`
	API         *API           `json:"api,omitempty"`
	Impact      *Impact        `json:"impact,omitempty"`
	Tests       []AffectedTest `json:"tests,omitempty"`
	Transitions *TransitionSet `json:"transitions,omitempty"`
	Truncated   bool           `json:"truncated"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
}

// Transitions projects normalized fact changes into stable keyed operations
// suitable for the existing d3-graphviz enter/update/exit animation model.
func Transitions(facts Facts) TransitionSet {
	var result TransitionSet
	for _, symbol := range facts.Symbols.Added {
		result.Nodes = append(result.Nodes, Transition{ID: symbol.ID, Status: "added"})
	}
	for _, symbol := range facts.Symbols.Removed {
		result.Nodes = append(result.Nodes, Transition{ID: symbol.ID, Status: "removed"})
	}
	for _, change := range facts.Symbols.Changed {
		result.Nodes = append(result.Nodes, Transition{ID: change.After.ID, Status: "changed"})
	}
	for _, edge := range facts.Edges.Added {
		result.Edges = append(result.Edges, Transition{ID: edge.ID, Status: "added"})
	}
	for _, edge := range facts.Edges.Removed {
		result.Edges = append(result.Edges, Transition{ID: edge.ID, Status: "removed"})
	}
	for _, change := range facts.Edges.Changed {
		result.Edges = append(result.Edges, Transition{ID: change.After.ID, Status: "changed"})
	}
	compare := func(a, b Transition) int {
		if value := strings.Compare(a.ID, b.ID); value != 0 {
			return value
		}
		return strings.Compare(a.Status, b.Status)
	}
	slices.SortFunc(result.Nodes, compare)
	slices.SortFunc(result.Edges, compare)
	return result
}

// Digest returns a stable digest of one normalized graph snapshot independent
// of the temporary worktree used to materialize it.
func Digest(snapshot graph.Snapshot) (string, error) {
	canonical := graph.Snapshot{
		Schema:      snapshot.Schema,
		Units:       append([]graph.Unit(nil), snapshot.Units...),
		Documents:   append([]graph.Document(nil), snapshot.Documents...),
		Symbols:     append([]graph.Symbol(nil), snapshot.Symbols...),
		Occurrences: append([]graph.Occurrence(nil), snapshot.Occurrences...),
		Edges:       append([]graph.Edge(nil), snapshot.Edges...),
	}
	graph.SortSnapshot(&canonical)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("weave.snapshot/v1\x00"), encoded...))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Compare returns a deterministic, independently bounded delta for every fact
// family. A limit applies to each added/removed/changed collection so one noisy
// family cannot hide every other kind of change.
func Compare(ctx context.Context, before, after graph.Snapshot, limit int) (Facts, error) {
	if limit < 1 {
		limit = 100
	}
	var result Facts
	var err error
	if result.Units, err = compare(ctx, before.Units, after.Units, limit, func(v graph.Unit) string { return v.ID }); err != nil {
		return Facts{}, err
	}
	if result.Documents, err = compare(ctx, before.Documents, after.Documents, limit, func(v graph.Document) string { return v.ID }); err != nil {
		return Facts{}, err
	}
	if result.Symbols, err = compare(ctx, before.Symbols, after.Symbols, limit, func(v graph.Symbol) string { return v.ID }); err != nil {
		return Facts{}, err
	}
	if result.Occurrences, err = compare(ctx, before.Occurrences, after.Occurrences, limit, func(v graph.Occurrence) string { return v.ID }); err != nil {
		return Facts{}, err
	}
	if result.Edges, err = compare(ctx, before.Edges, after.Edges, limit, func(v graph.Edge) string { return v.ID }); err != nil {
		return Facts{}, err
	}
	Limit(&result, limit)
	return result, nil
}

func compare[T any](ctx context.Context, before, after []T, limit int, id func(T) string) (Delta[T], error) {
	left := make(map[string]T, len(before))
	for i, value := range before {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return Delta[T]{}, err
			}
		}
		left[id(value)] = value
	}
	right := make(map[string]T, len(after))
	for i, value := range after {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return Delta[T]{}, err
			}
		}
		right[id(value)] = value
	}
	ids := make([]string, 0, len(left)+len(right))
	for value := range left {
		ids = append(ids, value)
	}
	for value := range right {
		if _, exists := left[value]; !exists {
			ids = append(ids, value)
		}
	}
	slices.Sort(ids)
	result := Delta[T]{}
	for i, value := range ids {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return Delta[T]{}, err
			}
		}
		old, hadOld := left[value]
		current, hasCurrent := right[value]
		switch {
		case !hadOld:
			appendBounded(&result.Added, current, limit)
		case !hasCurrent:
			appendBounded(&result.Removed, old, limit)
		case !reflect.DeepEqual(old, current):
			appendBounded(&result.Changed, Change[T]{Before: old, After: current}, limit)
		}
	}
	return result, nil
}

func appendBounded[T any](values *[]T, value T, limit int) {
	// Retain one sentinel beyond the public limit. truncated observes it and the
	// caller trims it before returning.
	if len(*values) <= limit {
		*values = append(*values, value)
	}
}

// Limit trims the sentinel retained by Compare and returns whether anything was
// omitted. It is separate so generic collection sizing remains straightforward.
func Limit(facts *Facts, limit int) bool {
	if limit < 1 {
		limit = 100
	}
	truncated := false
	trim := func(length int) bool { return length > limit }
	trimDelta := func(added, removed, changed int) {
		truncated = truncated || trim(added) || trim(removed) || trim(changed)
	}
	trimDelta(len(facts.Units.Added), len(facts.Units.Removed), len(facts.Units.Changed))
	trimDelta(len(facts.Documents.Added), len(facts.Documents.Removed), len(facts.Documents.Changed))
	trimDelta(len(facts.Symbols.Added), len(facts.Symbols.Removed), len(facts.Symbols.Changed))
	trimDelta(len(facts.Occurrences.Added), len(facts.Occurrences.Removed), len(facts.Occurrences.Changed))
	trimDelta(len(facts.Edges.Added), len(facts.Edges.Removed), len(facts.Edges.Changed))
	facts.Units = trimTo(facts.Units, limit)
	facts.Documents = trimTo(facts.Documents, limit)
	facts.Symbols = trimTo(facts.Symbols, limit)
	facts.Occurrences = trimTo(facts.Occurrences, limit)
	facts.Edges = trimTo(facts.Edges, limit)
	facts.Truncated = truncated
	return truncated
}

func trimTo[T any](delta Delta[T], limit int) Delta[T] {
	if len(delta.Added) > limit {
		delta.Added = delta.Added[:limit]
	}
	if len(delta.Removed) > limit {
		delta.Removed = delta.Removed[:limit]
	}
	if len(delta.Changed) > limit {
		delta.Changed = delta.Changed[:limit]
	}
	return delta
}

// Surfaces projects only provider-owned public surface fingerprints. Empty
// fingerprints are absent capability, not an empty API.
func Surfaces(before, after graph.Snapshot, limit int) API {
	if limit < 1 {
		limit = 100
	}
	left := make(map[string]graph.Unit, len(before.Units))
	right := make(map[string]graph.Unit, len(after.Units))
	for _, unit := range before.Units {
		left[unit.ID] = unit
	}
	for _, unit := range after.Units {
		right[unit.ID] = unit
	}
	ids := make([]string, 0, len(left)+len(right))
	for id := range left {
		ids = append(ids, id)
	}
	for id := range right {
		if _, ok := left[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	result := API{}
	for _, id := range ids {
		old, hadOld := left[id]
		current, hasCurrent := right[id]
		var value APISurface
		switch {
		case hadOld && hasCurrent && old.SurfaceFingerprint != current.SurfaceFingerprint && (old.SurfaceFingerprint != "" || current.SurfaceFingerprint != ""):
			value = APISurface{UnitID: id, Provider: current.Provider, Change: "changed", Before: old.SurfaceFingerprint, After: current.SurfaceFingerprint}
		case hadOld && !hasCurrent && old.SurfaceFingerprint != "":
			value = APISurface{UnitID: id, Provider: old.Provider, Change: "removed", Before: old.SurfaceFingerprint}
		case !hadOld && hasCurrent && current.SurfaceFingerprint != "":
			value = APISurface{UnitID: id, Provider: current.Provider, Change: "added", After: current.SurfaceFingerprint}
		default:
			continue
		}
		value.Evidence = "provider-surface-fingerprint"
		value.Compatibility = "unknown"
		if len(result.Surfaces) == limit {
			result.Truncated = true
			continue
		}
		result.Surfaces = append(result.Surfaces, value)
	}
	return result
}

// CurrentRoots derives traversal roots only from facts present in the current
// graph. Removed-only facts remain represented in Facts but cannot be traversed
// through the head snapshot.
func CurrentRoots(current graph.Snapshot, delta Facts) []string {
	root := map[string]bool{}
	for _, symbol := range delta.Symbols.Added {
		root[symbol.ID] = true
	}
	for _, change := range delta.Symbols.Changed {
		root[change.After.ID] = true
	}
	for _, occurrence := range delta.Occurrences.Added {
		root[occurrence.SymbolID] = true
	}
	for _, change := range delta.Occurrences.Changed {
		root[change.After.SymbolID] = true
	}
	for _, edge := range delta.Edges.Added {
		root[edge.From], root[edge.To] = true, true
	}
	for _, change := range delta.Edges.Changed {
		root[change.After.From], root[change.After.To] = true, true
	}
	changedUnits := map[string]bool{}
	changedDocuments := map[string]bool{}
	for _, unit := range delta.Units.Added {
		changedUnits[unit.ID] = true
	}
	for _, change := range delta.Units.Changed {
		changedUnits[change.After.ID] = true
	}
	for _, document := range delta.Documents.Added {
		changedDocuments[document.ID] = true
	}
	for _, change := range delta.Documents.Changed {
		changedDocuments[change.After.ID] = true
	}
	for _, symbol := range current.Symbols {
		if changedUnits[symbol.UnitID] || changedDocuments[symbol.DocumentID] {
			root[symbol.ID] = true
		}
	}
	values := make([]string, 0, len(root))
	for id := range root {
		if strings.TrimSpace(id) != "" {
			values = append(values, id)
		}
	}
	slices.Sort(values)
	return values
}
