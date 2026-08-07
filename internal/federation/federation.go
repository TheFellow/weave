// Package federation executes bounded deterministic queries over cataloged indexes.
package federation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/aggregate"
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
	entry      catalog.Entry
	generation string
	db         *storage.DB
}

// Store implements query.Store by merging independent member databases.
type Store struct {
	members     []member
	diagnostics []string
	sources     map[string]Source
	partial     bool
	accelerator *aggregate.DB
	cacheStatus aggregate.Status
	refresh     GenerationRefresher
}

// Refresher makes one catalog worktree current before its database is observed.
type Refresher func(context.Context, string) error

// GenerationRefresher makes a worktree current and returns the deterministic
// authoritative freshness generation used to validate a disposable aggregate.
type GenerationRefresher func(context.Context, string) (string, error)

// Open selects and opens at most maxRepositories independent catalog members.
func Open(ctx context.Context, catalogPath string, selectors []string, maxRepositories int) (*Store, error) {
	return open(ctx, catalogPath, selectors, maxRepositories, nil, false, "")
}

// OpenFresh refreshes every selected member and excludes any member that cannot
// prove freshness. Healthy members remain available as explicit partial results.
func OpenFresh(ctx context.Context, catalogPath string, selectors []string, maxRepositories int, refresh Refresher) (*Store, error) {
	var withGeneration GenerationRefresher
	if refresh != nil {
		withGeneration = func(ctx context.Context, root string) (string, error) {
			return "uncached", refresh(ctx, root)
		}
	}
	return open(ctx, catalogPath, selectors, maxRepositories, withGeneration, true, "")
}

// OpenFreshAccelerated proves freshness for every selected worktree, then uses
// or materializes an exact disposable hot read projection. Cache failures are
// reported diagnostically and fall back to authoritative federation.
func OpenFreshAccelerated(ctx context.Context, catalogPath string, selectors []string, maxRepositories int, aggregateDirectory string, refresh GenerationRefresher) (*Store, error) {
	if aggregateDirectory == "" {
		return nil, fmt.Errorf("aggregate directory is required")
	}
	return open(ctx, catalogPath, selectors, maxRepositories, refresh, true, aggregateDirectory)
}

func open(ctx context.Context, catalogPath string, selectors []string, maxRepositories int, refresh GenerationRefresher, requireFresh bool, aggregateDirectory string) (*Store, error) {
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
	result := &Store{sources: map[string]Source{}, refresh: refresh}
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
			generation, refreshErr := refresh(ctx, entry.Root)
			if refreshErr != nil {
				result.partial = true
				result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: excluded: refresh failed: "+refreshErr.Error())
				continue
			}
			if aggregateDirectory != "" && generation == "" {
				result.partial = true
				result.diagnostics = append(result.diagnostics, entry.Identity+" ["+entry.WorktreeID+"]: excluded: freshness returned no aggregate generation")
				continue
			}
			result.members = append(result.members, member{entry: entry, generation: generation})
		} else {
			result.members = append(result.members, member{entry: entry})
		}
	}
	if aggregateDirectory != "" && len(result.members) != 0 {
		sources := result.aggregateSources(false)
		if db, status, cacheErr := aggregate.Open(ctx, aggregateDirectory, sources, 250*time.Millisecond); cacheErr == nil {
			result.accelerator, result.cacheStatus = db, status
			slices.Sort(result.diagnostics)
			return result, nil
		}
	}
	healthy := result.members[:0]
	for _, pending := range result.members {
		db, openErr := storage.Open(ctx, pending.entry.DatabasePath, storage.Options{MustExist: true, Timeout: 250 * time.Millisecond})
		if openErr != nil {
			result.partial = true
			result.diagnostics = append(result.diagnostics, pending.entry.Identity+" ["+pending.entry.WorktreeID+"]: index unavailable: "+openErr.Error())
			continue
		}
		pending.db = db
		if aggregateDirectory != "" {
			databaseGeneration, generationErr := db.Generation(ctx)
			if generationErr != nil || databaseGeneration != pending.generation {
				_ = db.Close()
				result.partial = true
				detail := fmt.Sprintf("database generation %q does not match freshness generation %q", databaseGeneration, pending.generation)
				if generationErr != nil {
					detail = generationErr.Error()
				}
				result.diagnostics = append(result.diagnostics, pending.entry.Identity+" ["+pending.entry.WorktreeID+"]: index generation unavailable: "+detail)
				continue
			}
		}
		healthy = append(healthy, pending)
	}
	result.members = healthy
	if aggregateDirectory != "" && len(result.members) != 0 {
		db, status, cacheErr := aggregate.Ensure(ctx, aggregateDirectory, result.aggregateSources(true), 2*time.Second)
		if cacheErr == nil {
			result.accelerator, result.cacheStatus = db, status
			for i := range result.members {
				if result.members[i].db != nil {
					_ = result.members[i].db.Close()
					result.members[i].db = nil
				}
			}
		} else {
			result.diagnostics = append(result.diagnostics, "machine aggregate unavailable; using authoritative federation: "+cacheErr.Error())
			result.restoreReleasedMembers(ctx)
		}
	}
	slices.Sort(result.diagnostics)
	return result, nil
}

func (s *Store) aggregateSources(withStores bool) []aggregate.Source {
	result := make([]aggregate.Source, 0, len(s.members))
	for i := range s.members {
		member := &s.members[i]
		source := aggregate.Source{
			Key: member.entry.Key, Repository: member.entry.Identity, WorktreeID: member.entry.WorktreeID,
			Root: member.entry.Root, DatabasePath: member.entry.DatabasePath, Generation: member.generation,
		}
		if withStores {
			source.Store = member.db
			source.Release = func() error {
				if member.db == nil {
					return nil
				}
				err := member.db.Close()
				member.db = nil
				return err
			}
			source.Validate = func(ctx context.Context) (string, error) {
				if s.refresh == nil {
					return "", errors.New("freshness generation revalidation is unavailable")
				}
				return s.refresh(ctx, member.entry.Root)
			}
		}
		result = append(result, source)
	}
	return result
}

func (s *Store) restoreReleasedMembers(ctx context.Context) {
	healthy := s.members[:0]
	for _, member := range s.members {
		if member.db == nil {
			generation, err := s.refresh(ctx, member.entry.Root)
			if err != nil {
				s.partial = true
				s.diagnostics = append(s.diagnostics, member.entry.Identity+" ["+member.entry.WorktreeID+"]: excluded after aggregate failure: "+err.Error())
				continue
			}
			member.generation = generation
			db, err := storage.Open(ctx, member.entry.DatabasePath, storage.Options{MustExist: true, Timeout: 250 * time.Millisecond})
			if err != nil {
				s.partial = true
				s.diagnostics = append(s.diagnostics, member.entry.Identity+" ["+member.entry.WorktreeID+"]: index unavailable after aggregate failure: "+err.Error())
				continue
			}
			databaseGeneration, generationErr := db.Generation(ctx)
			if generationErr != nil || databaseGeneration != generation {
				_ = db.Close()
				s.partial = true
				detail := fmt.Sprintf("database generation %q does not match freshness generation %q", databaseGeneration, generation)
				if generationErr != nil {
					detail = generationErr.Error()
				}
				s.diagnostics = append(s.diagnostics, member.entry.Identity+" ["+member.entry.WorktreeID+"]: excluded after aggregate failure: "+detail)
				continue
			}
			member.db = db
		}
		healthy = append(healthy, member)
	}
	s.members = healthy
}

func (s *Store) Close() error {
	var failures []string
	if s.accelerator != nil {
		if err := s.accelerator.Close(); err != nil {
			failures = append(failures, err.Error())
		}
	}
	for _, member := range s.members {
		if member.db != nil {
			if err := member.db.Close(); err != nil {
				failures = append(failures, err.Error())
			}
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
	if s.accelerator != nil {
		values := s.accelerator.Sources()
		result := make([]Source, len(values))
		for i, value := range values {
			result[i] = Source(value)
		}
		return result
	}
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

// Accelerated reports whether this store is serving the exact validated
// machine aggregate rather than fanning queries out to worktree databases.
func (s *Store) Accelerated() bool { return s.accelerator != nil }

// CacheStatus describes the validated aggregate generation, when accelerated.
func (s *Store) CacheStatus() aggregate.Status { return s.cacheStatus }

// SourcesFor returns canonical repository provenance already observed for one
// fact. Query composition layers call this only after fetching the fact.
func (s *Store) SourcesFor(kind, id string) []Source {
	var result []Source
	for _, source := range s.Sources() {
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
	if s.accelerator != nil {
		value, ok, err := s.accelerator.Symbol(ctx, id)
		if err == nil {
			return value, ok, nil
		}
		if err := s.fallbackAccelerator(ctx, err); err != nil {
			return graph.Symbol{}, false, err
		}
	}
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
	if s.accelerator != nil {
		return graph.Document{}, false, fmt.Errorf("machine aggregate does not contain documents")
	}
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
	if s.accelerator != nil {
		values, truncated, err := s.accelerator.FindSymbols(ctx, value, limit)
		if err == nil {
			return values, truncated, nil
		}
		if err := s.fallbackAccelerator(ctx, err); err != nil {
			return nil, false, err
		}
	}
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
	if s.accelerator != nil {
		return nil, false, fmt.Errorf("machine aggregate does not contain occurrences")
	}
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
	if s.accelerator != nil {
		var values []graph.Edge
		var truncated bool
		var err error
		if forward {
			values, truncated, err = s.accelerator.EdgesFrom(ctx, id, kinds, limit)
		} else {
			values, truncated, err = s.accelerator.EdgesTo(ctx, id, kinds, limit)
		}
		if err == nil {
			return values, truncated, nil
		}
		if err := s.fallbackAccelerator(ctx, err); err != nil {
			return nil, false, err
		}
	}
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

func (s *Store) fallbackAccelerator(ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	if s.accelerator != nil {
		_ = s.accelerator.Close()
		s.accelerator = nil
	}
	selected := len(s.members)
	s.diagnostics = append(s.diagnostics, "machine aggregate query failed; using authoritative federation: "+cause.Error())
	s.restoreReleasedMembers(ctx)
	if selected != 0 && len(s.members) == 0 {
		return fmt.Errorf("machine aggregate query failed and authoritative federation could not be restored: %w", cause)
	}
	return nil
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
