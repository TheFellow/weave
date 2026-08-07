// Package federation executes bounded deterministic queries over cataloged indexes.
package federation

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
)

// Source records the repository provenance of one returned fact.
type Source struct {
	Kind       string `json:"kind"`
	FactID     string `json:"fact_id"`
	Repository string `json:"repository"`
	WorktreeID string `json:"worktree_id"`
	Root       string `json:"root"`
}

type member struct {
	entry catalog.Entry
	db    *storage.DB
}

// Store implements query.Store by merging independent member databases.
type Store struct {
	members     []member
	diagnostics []string
	sources     map[string]Source
	partial     bool
}

// Refresher makes one catalog worktree current before its database is observed.
type Refresher func(context.Context, string) error

// Open selects and opens at most maxRepositories independent catalog members.
func Open(ctx context.Context, catalogPath string, selectors []string, maxRepositories int) (*Store, error) {
	return open(ctx, catalogPath, selectors, maxRepositories, nil, false)
}

// OpenFresh refreshes every selected member and excludes any member that cannot
// prove freshness. Healthy members remain available as explicit partial results.
func OpenFresh(ctx context.Context, catalogPath string, selectors []string, maxRepositories int, refresh Refresher) (*Store, error) {
	return open(ctx, catalogPath, selectors, maxRepositories, refresh, true)
}

func open(ctx context.Context, catalogPath string, selectors []string, maxRepositories int, refresh Refresher, requireFresh bool) (*Store, error) {
	if maxRepositories < 1 || maxRepositories > 256 {
		return nil, fmt.Errorf("max repositories must be between 1 and 256")
	}
	catalogDB, err := catalog.Open(ctx, catalogPath, 2*time.Second)
	if err != nil {
		return nil, err
	}
	entries, err := catalogDB.List(ctx)
	closeErr := catalogDB.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	selected := make([]catalog.Entry, 0, len(entries))
	matched := make(map[string]bool, len(selectors))
	for _, entry := range entries {
		entryMatched := len(selectors) == 0
		for _, selector := range selectors {
			if selector == entry.Key || selector == entry.Identity || selector == entry.Root {
				matched[selector] = true
				entryMatched = true
			}
		}
		if entryMatched {
			selected = append(selected, entry)
		}
	}
	var unmatched []string
	for _, selector := range selectors {
		if !matched[selector] {
			unmatched = append(unmatched, selector)
		}
	}
	slices.Sort(unmatched)
	unmatched = slices.Compact(unmatched)
	if len(unmatched) > 0 {
		return nil, fmt.Errorf("catalog selectors did not match registered repositories: %s", strings.Join(unmatched, ", "))
	}
	if len(selected) > maxRepositories {
		return nil, fmt.Errorf("catalog query selects %d repositories, exceeds --max-repos %d", len(selected), maxRepositories)
	}
	result := &Store{sources: map[string]Source{}}
	for _, entry := range selected {
		if entry.Missing {
			result.partial = true
			result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: missing worktree")
			continue
		}
		if entry.Stale {
			result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: stale catalog state: "+entry.Diagnostic)
		}
		if requireFresh {
			if refresh == nil {
				result.partial = true
				result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: excluded: no freshness provider configured")
				continue
			}
			if refreshErr := refresh(ctx, entry.Root); refreshErr != nil {
				result.partial = true
				result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: excluded: refresh failed: "+refreshErr.Error())
				continue
			}
		}
		db, openErr := storage.Open(ctx, entry.DatabasePath, storage.Options{MustExist: true, Timeout: 250 * time.Millisecond})
		if openErr != nil {
			result.partial = true
			result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: index unavailable: "+openErr.Error())
			continue
		}
		result.members = append(result.members, member{entry: entry, db: db})
	}
	slices.Sort(result.diagnostics)
	return result, nil
}

func (s *Store) Close() error {
	var failures []string
	for _, member := range s.members {
		if err := member.db.Close(); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("close federation: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (s *Store) Diagnostics() []string {
	result := append([]string(nil), s.diagnostics...)
	slices.Sort(result)
	return slices.Compact(result)
}

// Partial reports whether at least one selected member was excluded or failed
// while serving facts. Informational stale-catalog diagnostics alone do not make
// a successfully refreshed result partial.
func (s *Store) Partial() bool { return s.partial }

func (s *Store) Sources() []Source {
	result := make([]Source, 0, len(s.sources))
	for _, source := range s.sources {
		result = append(result, source)
	}
	slices.SortFunc(result, func(a, b Source) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if a.FactID != b.FactID {
			return strings.Compare(a.FactID, b.FactID)
		}
		if a.Repository != b.Repository {
			return strings.Compare(a.Repository, b.Repository)
		}
		return strings.Compare(a.WorktreeID, b.WorktreeID)
	})
	return result
}

// SourcesFor returns canonical repository provenance already observed for one
// fact. Query composition layers call this only after fetching the fact.
func (s *Store) SourcesFor(kind, id string) []Source {
	var result []Source
	for _, source := range s.sources {
		if source.Kind == kind && source.FactID == id {
			result = append(result, source)
		}
	}
	slices.SortFunc(result, func(a, b Source) int {
		if a.Repository != b.Repository {
			return strings.Compare(a.Repository, b.Repository)
		}
		if a.WorktreeID != b.WorktreeID {
			return strings.Compare(a.WorktreeID, b.WorktreeID)
		}
		return strings.Compare(a.Root, b.Root)
	})
	return result
}

func (s *Store) record(kind, id string, entry catalog.Entry) {
	key := kind + "\x00" + id + "\x00" + entry.Key
	s.sources[key] = Source{Kind: kind, FactID: id, Repository: entry.Identity, WorktreeID: entry.WorktreeID, Root: entry.Root}
}

func (s *Store) Symbol(ctx context.Context, id string) (graph.Symbol, bool, error) {
	var result graph.Symbol
	found := false
	for _, member := range s.members {
		symbol, ok, err := member.db.Symbol(ctx, id)
		if err != nil {
			s.partial = true
			s.diagnostics = append(s.diagnostics, member.entry.Identity+": "+err.Error())
			continue
		}
		if !ok {
			continue
		}
		s.record("symbol", symbol.ID, member.entry)
		if !found || symbolKey(symbol) < symbolKey(result) {
			result, found = symbol, true
		}
	}
	return result, found, nil
}

// Document returns the canonical materialized document while retaining every
// repository that supplied the stable document ID as provenance.
func (s *Store) Document(ctx context.Context, id string) (graph.Document, bool, error) {
	var result graph.Document
	found := false
	for _, member := range s.members {
		document, ok, err := member.db.Document(ctx, id)
		if err != nil {
			s.partial = true
			s.diagnostics = append(s.diagnostics, member.entry.Identity+": "+err.Error())
			continue
		}
		if !ok {
			continue
		}
		s.record("document", document.ID, member.entry)
		if !found || document.Path < result.Path || (document.Path == result.Path && document.UnitID < result.UnitID) {
			result, found = document, true
		}
	}
	return result, found, nil
}

func (s *Store) FindSymbols(ctx context.Context, value string, limit int) ([]graph.Symbol, bool, error) {
	byID := map[string]graph.Symbol{}
	truncated := false
	for _, member := range s.members {
		values, memberTruncated, err := member.db.FindSymbols(ctx, value, limit)
		if err != nil {
			s.partial = true
			s.diagnostics = append(s.diagnostics, member.entry.Identity+": "+err.Error())
			continue
		}
		truncated = truncated || memberTruncated
		for _, symbol := range values {
			s.record("symbol", symbol.ID, member.entry)
			if current, exists := byID[symbol.ID]; !exists || symbolKey(symbol) < symbolKey(current) {
				byID[symbol.ID] = symbol
			}
		}
	}
	results := make([]graph.Symbol, 0, len(byID))
	for _, symbol := range byID {
		results = append(results, symbol)
	}
	normalized := graph.NormalizeName(value)
	slices.SortFunc(results, func(a, b graph.Symbol) int {
		rank := func(symbol graph.Symbol) int {
			switch {
			case symbol.ID == value || symbol.StableName == value:
				return 0
			case symbol.NormalizedName == normalized:
				return 1
			case strings.HasPrefix(symbol.NormalizedName, normalized):
				return 2
			default:
				return 3
			}
		}
		if left, right := rank(a), rank(b); left != right {
			return left - right
		}
		return strings.Compare(symbolKey(a), symbolKey(b))
	})
	if len(results) > limit {
		results, truncated = results[:limit], true
	}
	return results, truncated, nil
}

func (s *Store) Occurrences(ctx context.Context, symbolID string, roles []string, limit int) ([]graph.Occurrence, bool, error) {
	byID := map[string]graph.Occurrence{}
	truncated := false
	for _, member := range s.members {
		values, memberTruncated, err := member.db.Occurrences(ctx, symbolID, roles, limit)
		if err != nil {
			s.partial = true
			s.diagnostics = append(s.diagnostics, member.entry.Identity+": "+err.Error())
			continue
		}
		truncated = truncated || memberTruncated
		for _, occurrence := range values {
			key := occurrence.ID + "\x00" + member.entry.Key
			byID[key] = occurrence
			s.record("occurrence", occurrence.ID, member.entry)
		}
	}
	results := make([]graph.Occurrence, 0, len(byID))
	for _, occurrence := range byID {
		results = append(results, occurrence)
	}
	slices.SortFunc(results, func(a, b graph.Occurrence) int {
		if a.DocumentID != b.DocumentID {
			return strings.Compare(a.DocumentID, b.DocumentID)
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(results) > limit {
		results, truncated = results[:limit], true
	}
	return results, truncated, nil
}

func (s *Store) EdgesFrom(ctx context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return s.edges(ctx, true, id, kinds, limit)
}

func (s *Store) EdgesTo(ctx context.Context, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	return s.edges(ctx, false, id, kinds, limit)
}

func (s *Store) edges(ctx context.Context, forward bool, id string, kinds []graph.EdgeKind, limit int) ([]graph.Edge, bool, error) {
	byKey := map[string]graph.Edge{}
	truncated := false
	for _, member := range s.members {
		var values []graph.Edge
		var memberTruncated bool
		var err error
		if forward {
			values, memberTruncated, err = member.db.EdgesFrom(ctx, id, kinds, limit)
		} else {
			values, memberTruncated, err = member.db.EdgesTo(ctx, id, kinds, limit)
		}
		if err != nil {
			s.partial = true
			s.diagnostics = append(s.diagnostics, member.entry.Identity+": "+err.Error())
			continue
		}
		truncated = truncated || memberTruncated
		for _, edge := range values {
			key := edgeKey(edge)
			canonical, exists := byKey[key]
			if !exists {
				byKey[key] = edge
				canonical = edge
			}
			s.record("edge", canonical.ID, member.entry)
		}
	}
	results := make([]graph.Edge, 0, len(byKey))
	for _, edge := range byKey {
		results = append(results, edge)
	}
	slices.SortFunc(results, graph.CompareEdges)
	if len(results) > limit {
		results, truncated = results[:limit], true
	}
	return results, truncated, nil
}

func symbolKey(symbol graph.Symbol) string {
	return symbol.NormalizedName + "\x00" + symbol.StableName + "\x00" + symbol.ID + "\x00" + symbol.DocumentID
}

func edgeKey(edge graph.Edge) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d:%d:%d:%d:%d:%d",
		edge.From, edge.Kind, edge.To, edge.Evidence, edge.DocumentID, edge.Provider,
		edge.Range.Start.Line, edge.Range.Start.Column, edge.Range.Start.Byte,
		edge.Range.End.Line, edge.Range.End.Column, edge.Range.End.Byte)
}
