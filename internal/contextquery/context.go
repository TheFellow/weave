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
	Scope           string
	Limit           int
	ContextLines    int
	MaxSourceBytes  int
	FullDefinitions bool
	// LexicalTerms are normalized discovery terms used only to anchor a file
	// entity's current-source excerpt. They do not create graph evidence.
	LexicalTerms []string
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
		if focus.Kind == "file" && item.Role == "focus" && len(options.LexicalTerms) != 0 {
			item.Source = loader.lexicalExcerpt(ctx, item.Repositories[0], document, options.LexicalTerms)
		} else if options.FullDefinitions && item.Role == "definition" {
			item.Source = loader.definitionExcerpt(ctx, item.Repositories[0], document, item.Range, focus.Kind)
		} else {
			item.Source = loader.excerpt(ctx, item.Repositories[0], document, item.Range)
		}
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
	var storageTruncated bool
	var err error
	// Compiler-backed providers commonly emit both a specific relationship
	// (calls, implements, imports) and its underlying reference edge. Read a
	// bounded surplus so those duplicates can be collapsed before the caller's
	// result limit is applied.
	queryLimit := min(512, max(limit+1, limit*8))
	if outgoing {
		edges, storageTruncated, err = store.EdgesFrom(ctx, focus, nil, queryLimit)
	} else {
		edges, storageTruncated, err = store.EdgesTo(ctx, focus, nil, queryLimit)
	}
	if err != nil {
		return nil, false, err
	}
	byEndpoint := make(map[string]graph.Edge, len(edges))
	for _, edge := range edges {
		adjacent := edge.From
		if outgoing {
			adjacent = edge.To
		}
		current, exists := byEndpoint[adjacent]
		if !exists || preferRelationship(edge, current) {
			byEndpoint[adjacent] = edge
		}
	}
	edges = edges[:0]
	for _, edge := range byEndpoint {
		edges = append(edges, edge)
	}
	truncated := storageTruncated
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
		if lowValueReference(relationship) {
			continue
		}
		result = append(result, relationship)
	}
	focusSymbol, _, err := store.Symbol(ctx, focus)
	if err != nil {
		return nil, false, err
	}
	slices.SortFunc(result, func(left, right Relationship) int {
		if rank := relationshipRank(left.Edge.Kind) - relationshipRank(right.Edge.Kind); rank != 0 {
			return rank
		}
		leftProximity := relationshipProximity(focusSymbol, left)
		rightProximity := relationshipProximity(focusSymbol, right)
		if leftProximity != rightProximity {
			return rightProximity - leftProximity
		}
		leftName, rightName := relationshipName(left, outgoing), relationshipName(right, outgoing)
		if leftName != rightName {
			return strings.Compare(leftName, rightName)
		}
		return strings.Compare(left.Edge.ID, right.Edge.ID)
	})
	if len(result) > limit {
		result = result[:limit]
		truncated = true
	}
	return result, truncated, nil
}

func relationshipName(relationship Relationship, outgoing bool) string {
	if relationship.Entity != nil && relationship.Entity.Symbol.StableName != "" {
		return relationship.Entity.Symbol.StableName
	}
	if outgoing {
		return relationship.Edge.To
	}
	return relationship.Edge.From
}

func relationshipProximity(focus graph.Symbol, relationship Relationship) int {
	if relationship.Entity == nil {
		return 0
	}
	adjacent := relationship.Entity.Symbol
	score := commonPathSegments(focus.StableName, adjacent.StableName) * 10
	if focus.UnitID != "" && focus.UnitID == adjacent.UnitID {
		score += 1000
	}
	return score
}

func commonPathSegments(left, right string) int {
	leftParts, rightParts := strings.Split(left, "/"), strings.Split(right, "/")
	limit := min(len(leftParts), len(rightParts))
	count := 0
	for count < limit && leftParts[count] == rightParts[count] {
		count++
	}
	return count
}

func lowValueReference(relationship Relationship) bool {
	if relationship.Edge.Kind != graph.EdgeReferences || relationship.Entity == nil {
		return false
	}
	symbol := relationship.Entity.Symbol
	if symbol.Kind == "variable" || symbol.Kind == "parameter" {
		return true
	}
	switch symbol.StableName {
	case "true", "false", "nil", "bool", "byte", "complex64", "complex128", "error", "float32", "float64", "int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	default:
		return false
	}
}

func preferRelationship(candidate, current graph.Edge) bool {
	candidateRank, currentRank := relationshipRank(candidate.Kind), relationshipRank(current.Kind)
	return candidateRank < currentRank || (candidateRank == currentRank && candidate.ID < current.ID)
}

// relationshipRank keeps flow and contract edges ahead of raw references.
// The latter remain available when they are the only evidence for an endpoint,
// but no longer crowd calls out of a bounded agent context response.
func relationshipRank(kind graph.EdgeKind) int {
	switch kind {
	case graph.EdgeCalls:
		return 0
	case graph.EdgeImplements, graph.EdgeExtends, graph.EdgeInstantiates, graph.EdgeHandles:
		return 1
	case graph.EdgeTests, graph.EdgeDependsOn, graph.EdgeImports:
		return 2
	case graph.EdgeExposes, graph.EdgeGenerates, graph.EdgeDocuments, graph.EdgeLinksTo, graph.EdgeEmbeds, graph.EdgeResolvesTo:
		return 3
	case graph.EdgeReads, graph.EdgeWrites:
		return 4
	case graph.EdgeMemberOf, graph.EdgeDefines, graph.EdgeContains:
		return 5
	case graph.EdgeReferences:
		return 6
	default:
		return 7
	}
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
