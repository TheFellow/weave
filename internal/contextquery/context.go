// Package contextquery composes bounded source-rich views over Weave's graph.
package contextquery

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
)

const Schema = "weave.context/v1"

// Store is the existing graph surface required by a context query.
type Store interface {
	query.Store
	Occurrences(context.Context, string, []string, int) ([]graph.Occurrence, bool, error)
	Document(context.Context, string) (graph.Document, bool, error)
}

// Repository identifies the worktree whose current source may be read.
type Repository struct {
	Identity   string `json:"identity"`
	WorktreeID string `json:"worktree_id"`
	Root       string `json:"root"`
}

// Locator returns deterministic provenance for a fetched fact.
type Locator func(kind, id string) []Repository

// Options are independently bounded; Limit applies to each occurrence and
// relationship section rather than allowing one section to starve another.
type Options struct {
	Scope          string
	Limit          int
	ContextLines   int
	MaxSourceBytes int
}

type Result struct {
	Schema   string         `json:"schema"`
	Target   string         `json:"target"`
	Focus    Entity         `json:"focus"`
	Evidence []Evidence     `json:"evidence"`
	Incoming []Relationship `json:"incoming"`
	Outgoing []Relationship `json:"outgoing"`
	Metadata Metadata       `json:"metadata"`
}

type Entity struct {
	Symbol       graph.Symbol `json:"symbol"`
	Repositories []Repository `json:"repositories,omitempty"`
}

type Evidence struct {
	Kind         string          `json:"kind"`
	OccurrenceID string          `json:"occurrence_id,omitempty"`
	Role         string          `json:"role"`
	Range        graph.Range     `json:"range"`
	Provider     string          `json:"provider"`
	Confidence   graph.Evidence  `json:"evidence"`
	Document     *graph.Document `json:"document,omitempty"`
	Repositories []Repository    `json:"repositories,omitempty"`
	Source       SourceExcerpt   `json:"source"`
	factKind     string
	factID       string
	documentID   string
	documentPath string
}

type Relationship struct {
	Edge         graph.Edge      `json:"edge"`
	Entity       *Entity         `json:"entity,omitempty"`
	Document     *graph.Document `json:"document,omitempty"`
	Repositories []Repository    `json:"repositories,omitempty"`
	Source       *SourceExcerpt  `json:"source,omitempty"`
}

type Metadata struct {
	Scope       string     `json:"scope"`
	Freshness   Freshness  `json:"freshness"`
	SourceBytes int        `json:"source_bytes"`
	Truncation  Truncation `json:"truncation"`
}

type Freshness struct {
	Checked bool `json:"checked"`
	Current bool `json:"current"`
	Partial bool `json:"partial"`
}

type Truncation struct {
	Occurrences bool `json:"occurrences"`
	Incoming    bool `json:"incoming"`
	Outgoing    bool `json:"outgoing"`
	Source      bool `json:"source"`
}

// Build resolves one entity uniquely and composes only direct, bounded facts.
func Build(ctx context.Context, store Store, target string, options Options, locate Locator) (Result, error) {
	if options.Limit < 1 || options.Limit > 512 {
		return Result{}, errors.New("context limit must be between 1 and 512")
	}
	if options.ContextLines < 0 || options.ContextLines > 100 {
		return Result{}, errors.New("context lines must be between 0 and 100")
	}
	if options.MaxSourceBytes < 1 || options.MaxSourceBytes > 4<<20 {
		return Result{}, errors.New("maximum source bytes must be between 1 and 4194304")
	}
	focus, err := query.ResolveUnique(ctx, store, target)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Schema: Schema, Target: target, Metadata: Metadata{Scope: options.Scope},
		Evidence: []Evidence{}, Incoming: []Relationship{}, Outgoing: []Relationship{},
	}
	result.Focus = entity(focus, locate)

	definitions, definitionsTruncated, err := store.Occurrences(ctx, focus.ID, []string{"definition"}, options.Limit+1)
	if err != nil {
		return Result{}, err
	}
	occurrences, occurrencesTruncated, err := store.Occurrences(ctx, focus.ID, nil, options.Limit+1)
	if err != nil {
		return Result{}, err
	}
	byID := make(map[string]graph.Occurrence, len(definitions)+len(occurrences))
	for _, occurrence := range append(definitions, occurrences...) {
		byID[occurrence.ID] = occurrence
	}
	ordered := make([]graph.Occurrence, 0, len(byID))
	for _, occurrence := range byID {
		ordered = append(ordered, occurrence)
	}
	slices.SortFunc(ordered, compareOccurrences)
	if len(ordered) > options.Limit {
		ordered = ordered[:options.Limit]
		result.Metadata.Truncation.Occurrences = true
	}
	result.Metadata.Truncation.Occurrences = result.Metadata.Truncation.Occurrences || definitionsTruncated || occurrencesTruncated
	for _, occurrence := range ordered {
		result.Evidence = append(result.Evidence, evidenceForOccurrence(occurrence))
	}
	if !hasDefinition(result.Evidence) && focus.DocumentID != "" {
		if len(result.Evidence) >= options.Limit {
			result.Evidence = result.Evidence[:options.Limit-1]
			result.Metadata.Truncation.Occurrences = true
		}
		result.Evidence = append([]Evidence{{
			Kind: "anchor", Role: "definition", Range: focus.Definition,
			Provider: focus.Provider, Confidence: focus.Evidence,
			factKind: "symbol", factID: focus.ID, documentID: focus.DocumentID,
		}}, result.Evidence...)
	}
	if len(result.Evidence) == 0 {
		if sourcePath := entitySourcePath(focus); sourcePath != "" {
			result.Evidence = append(result.Evidence, Evidence{
				Kind: "entity", Role: "focus",
				Range:    graph.Range{Start: graph.Position{Byte: -1}, End: graph.Position{Byte: -1}},
				Provider: focus.Provider, Confidence: focus.Evidence,
				factKind: "symbol", factID: focus.ID, documentPath: sourcePath,
			})
		}
	}

	result.Incoming, result.Metadata.Truncation.Incoming, err = relationships(ctx, store, focus.ID, false, options.Limit, locate)
	if err != nil {
		return Result{}, err
	}
	result.Outgoing, result.Metadata.Truncation.Outgoing, err = relationships(ctx, store, focus.ID, true, options.Limit, locate)
	if err != nil {
		return Result{}, err
	}

	loader := newSourceLoader(options.ContextLines, options.MaxSourceBytes)
	for index := range result.Evidence {
		item := &result.Evidence[index]
		var document graph.Document
		if item.documentPath != "" {
			document = graph.Document{Path: item.documentPath, Provider: item.Provider}
		} else if item.documentID == "" {
			item.Source = SourceExcerpt{Status: SourceUnavailable, Detail: "evidence has no document"}
			continue
		} else {
			var ok bool
			document, ok, err = store.Document(ctx, item.documentID)
			if err != nil {
				return Result{}, err
			}
			if !ok {
				item.Source = SourceExcerpt{Status: SourceMissingDocument, Detail: "document fact is not materialized"}
				continue
			}
			item.Document = &document
		}
		item.Repositories = repositories(locate, item.factKind, item.factID)
		if len(item.Repositories) == 0 {
			item.Repositories = repositories(locate, "document", document.ID)
		}
		if len(item.Repositories) == 0 {
			item.Source = SourceExcerpt{Status: SourceUnavailable, Path: document.Path, Detail: "repository provenance is unavailable"}
			continue
		}
		item.Source = loader.excerpt(ctx, item.Repositories[0], document, item.Range)
	}
	if err := hydrateRelationshipSources(ctx, store, result.Incoming, loader, locate); err != nil {
		return Result{}, err
	}
	if err := hydrateRelationshipSources(ctx, store, result.Outgoing, loader, locate); err != nil {
		return Result{}, err
	}
	result.Metadata.SourceBytes = loader.used
	result.Metadata.Truncation.Source = loader.truncated
	return result, nil
}

func hydrateRelationshipSources(ctx context.Context, store Store, values []Relationship, loader *sourceLoader, locate Locator) error {
	for index := range values {
		item := &values[index]
		if item.Edge.DocumentID == "" {
			continue
		}
		document, ok, err := store.Document(ctx, item.Edge.DocumentID)
		if err != nil {
			return err
		}
		if !ok {
			source := SourceExcerpt{Status: SourceMissingDocument, Detail: "relationship document fact is not materialized"}
			item.Source = &source
			continue
		}
		item.Document = &document
		if len(item.Repositories) == 0 {
			item.Repositories = repositories(locate, "document", document.ID)
		}
		if len(item.Repositories) == 0 {
			source := SourceExcerpt{Status: SourceUnavailable, Path: document.Path, Detail: "repository provenance is unavailable"}
			item.Source = &source
			continue
		}
		source := loader.excerpt(ctx, item.Repositories[0], document, item.Edge.Range)
		item.Source = &source
	}
	return nil
}

func relationships(ctx context.Context, store Store, focus string, outgoing bool, limit int, locate Locator) ([]Relationship, bool, error) {
	var edges []graph.Edge
	var truncated bool
	var err error
	if outgoing {
		edges, truncated, err = store.EdgesFrom(ctx, focus, nil, limit)
	} else {
		edges, truncated, err = store.EdgesTo(ctx, focus, nil, limit)
	}
	if err != nil {
		return nil, false, err
	}
	result := make([]Relationship, 0, len(edges))
	for _, edge := range edges {
		adjacent := edge.From
		if outgoing {
			adjacent = edge.To
		}
		relationship := Relationship{Edge: edge, Repositories: repositories(locate, "edge", edge.ID)}
		if symbol, ok, err := store.Symbol(ctx, adjacent); err != nil {
			return nil, false, err
		} else if ok {
			value := entity(symbol, locate)
			relationship.Entity = &value
		}
		result = append(result, relationship)
	}
	return result, truncated, nil
}

func entity(symbol graph.Symbol, locate Locator) Entity {
	return Entity{Symbol: symbol, Repositories: repositories(locate, "symbol", symbol.ID)}
}

func repositories(locate Locator, kind, id string) []Repository {
	if locate == nil || id == "" {
		return nil
	}
	values := append([]Repository(nil), locate(kind, id)...)
	slices.SortFunc(values, func(a, b Repository) int {
		if a.Identity != b.Identity {
			return strings.Compare(a.Identity, b.Identity)
		}
		if a.WorktreeID != b.WorktreeID {
			return strings.Compare(a.WorktreeID, b.WorktreeID)
		}
		return strings.Compare(a.Root, b.Root)
	})
	return slices.CompactFunc(values, func(a, b Repository) bool { return a == b })
}

func evidenceForOccurrence(occurrence graph.Occurrence) Evidence {
	return Evidence{
		Kind: "occurrence", OccurrenceID: occurrence.ID, Role: occurrence.Role,
		Range: occurrence.Range, Provider: occurrence.Provider, Confidence: occurrence.Evidence,
		factKind: "occurrence", factID: occurrence.ID, documentID: occurrence.DocumentID,
	}
}

func entitySourcePath(symbol graph.Symbol) string {
	switch symbol.Kind {
	case "file", "symlink", "asset", "resource":
		return symbol.StableName
	default:
		return ""
	}
}

func hasDefinition(values []Evidence) bool {
	for _, value := range values {
		if value.Role == "definition" {
			return true
		}
	}
	return false
}

func compareOccurrences(a, b graph.Occurrence) int {
	rank := func(role string) int {
		if role == "definition" {
			return 0
		}
		return 1
	}
	if left, right := rank(a.Role), rank(b.Role); left != right {
		return left - right
	}
	if a.DocumentID != b.DocumentID {
		return strings.Compare(a.DocumentID, b.DocumentID)
	}
	if a.Range.Start.Line != b.Range.Start.Line {
		return int(a.Range.Start.Line - b.Range.Start.Line)
	}
	if a.Range.Start.Column != b.Range.Start.Column {
		return int(a.Range.Start.Column - b.Range.Start.Column)
	}
	return strings.Compare(a.ID, b.ID)
}
