// Package explorer serves a human-only local view over Weave's existing graph
// application query and deterministic DOT renderer.
package explorer

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/dot"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
	"github.com/TheFellow/weave/internal/query"
)

const Schema = "weave.explorer/v1"

// Request is one bounded interactive neighborhood query.
type Request struct {
	Target    string           `json:"target"`
	Direction query.Direction  `json:"direction"`
	MaxDepth  int              `json:"max_depth"`
	Limit     int              `json:"limit"`
	MaxEdges  int              `json:"max_edges"`
	Kinds     []graph.EdgeKind `json:"kinds,omitempty"`
	Providers []string         `json:"providers,omitempty"`
	Evidence  []graph.Evidence `json:"evidence,omitempty"`
}

// DiffRequest asks the existing explorer application boundary for a bounded
// normalized graph transition. Omitted Head means the current dirty worktree.
type DiffRequest struct {
	Base     string           `json:"base"`
	Head     string           `json:"head,omitempty"`
	MaxDepth int              `json:"max_depth"`
	Limit    int              `json:"limit"`
	MaxEdges int              `json:"max_edges"`
	Kinds    []graph.EdgeKind `json:"kinds,omitempty"`
}

// Node maps a stable SVG element back to its exact semantic graph target.
type Node struct {
	ID       string         `json:"id"`
	SVGID    string         `json:"svg_id"`
	Label    string         `json:"label"`
	Kind     string         `json:"kind,omitempty"`
	Provider string         `json:"provider,omitempty"`
	Evidence graph.Evidence `json:"evidence,omitempty"`
}

// Edge maps one stable SVG element to every normalized fact collapsed into
// that visual edge.
type Edge struct {
	SVGID string       `json:"svg_id"`
	Facts []graph.Edge `json:"facts"`
}

// Options describes filter values understood by the current graph view.
type Options struct {
	Kinds     []graph.EdgeKind `json:"kinds"`
	Providers []string         `json:"providers"`
	Evidence  []graph.Evidence `json:"evidence"`
}

// Result is the complete presentation model for one rendered snapshot.
type Result struct {
	Schema      string   `json:"schema"`
	Target      string   `json:"target"`
	Focus       string   `json:"focus"`
	FocusLabel  string   `json:"focus_label"`
	DOT         string   `json:"dot"`
	Nodes       []Node   `json:"nodes"`
	Edges       []Edge   `json:"edges"`
	Options     Options  `json:"options"`
	Truncated   bool     `json:"truncated"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// DetailRequest asks for bounded current source and provenance for a selected
// graph entity. EdgeID narrows the result to one direct relationship while
// retaining the same safe context-query source loader.
type DetailRequest struct {
	Target         string `json:"target"`
	EdgeID         string `json:"edge_id,omitempty"`
	Limit          int    `json:"limit"`
	ContextLines   int    `json:"context_lines"`
	MaxSourceBytes int    `json:"max_source_bytes"`
}

// Detail is one source-rich node or edge inspector response.
type Detail struct {
	Schema       string                     `json:"schema"`
	Kind         string                     `json:"kind"`
	Context      *contextquery.Result       `json:"context,omitempty"`
	Relationship *contextquery.Relationship `json:"relationship,omitempty"`
}

// LinkList is the canonical authored-link state plus its optimistic revision.
type LinkList struct {
	Schema   string        `json:"schema"`
	Revision string        `json:"revision"`
	Links    []bridge.Link `json:"links"`
}

// LinkMutationResult describes exactly one authored-link mutation. Link is
// the affected declaration (including the declaration removed by remove), not
// an ambiguously partial canonical list. Call Links to reload canonical state.
type LinkMutationResult struct {
	Schema    string      `json:"schema"`
	Operation string      `json:"operation"`
	Revision  string      `json:"revision"`
	Link      bridge.Link `json:"link"`
}

// LinkMutation is a strict browser patch. Pointer fields preserve the
// distinction between omitted values and an intentional empty note.
type LinkMutation struct {
	ID       string          `json:"id"`
	Revision string          `json:"revision"`
	From     *string         `json:"from,omitempty"`
	To       *string         `json:"to,omitempty"`
	Kind     *graph.EdgeKind `json:"kind,omitempty"`
	Note     *string         `json:"note,omitempty"`
}

// Engine translates explorer requests into the same application invocation
// used by `weave graph`.
type Engine struct {
	app           application.Service
	base          application.Invocation
	initialTarget string
}

// New creates an explorer engine. Base fixes repository/catalog scope for the
// lifetime of the loopback server; browser requests cannot expand that scope.
func New(app application.Service, base application.Invocation) (*Engine, error) {
	if app == nil {
		return nil, fmt.Errorf("explorer application is nil")
	}
	if len(base.Arguments) != 1 || strings.TrimSpace(base.Arguments[0]) == "" {
		return nil, fmt.Errorf("explorer requires one initial graph target")
	}
	initialTarget := base.Arguments[0]
	base.Command = "graph"
	base.Arguments = nil
	base.JSON = false
	return &Engine{app: app, base: base, initialTarget: initialTarget}, nil
}

// InitialRequest returns the fixed command-line starting point and bounds.
func (engine *Engine) InitialRequest() Request {
	return Request{
		Target: engine.initialTarget, Direction: engine.base.Direction,
		MaxDepth: engine.base.MaxDepth, Limit: engine.base.Limit,
		MaxEdges: engine.base.MaxEdges, Kinds: append([]graph.EdgeKind(nil), engine.base.Kinds...),
	}
}

// Query executes, filters, and renders one current graph snapshot.
func (engine *Engine) Query(ctx context.Context, request Request) (Result, error) {
	request, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	invocation := engine.base
	invocation.Arguments = []string{request.Target}
	invocation.Direction = request.Direction
	invocation.MaxDepth = request.MaxDepth
	invocation.Limit = request.Limit
	invocation.MaxEdges = request.MaxEdges
	invocation.Kinds = append([]graph.EdgeKind(nil), request.Kinds...)
	response, err := engine.app.Execute(ctx, invocation)
	if err != nil {
		return Result{}, err
	}
	if len(response.Nodes) == 0 || response.Nodes[0] == "" {
		return Result{}, fmt.Errorf("graph query returned no focus node")
	}

	options := resultOptions(response.Edges)
	response = filterResponse(response, request)
	focus := response.Nodes[0]
	var rendered bytes.Buffer
	if err := dot.Write(&rendered, dot.View{
		Focus: focus, Nodes: response.Nodes, Symbols: response.Symbols,
		Edges: response.Edges, Truncated: response.Truncated, OmitTitle: true,
	}); err != nil {
		return Result{}, err
	}
	nodes, focusLabel := resultNodes(response, focus)
	return Result{
		Schema: Schema, Target: request.Target, Focus: focus, FocusLabel: focusLabel,
		DOT: rendered.String(), Nodes: nodes, Edges: resultEdges(response.Edges), Options: options,
		Truncated: response.Truncated, Diagnostics: response.Diagnostics,
	}, nil
}

// Detail executes the existing source-rich context use case. Edge selection
// starts at its exact from endpoint and then requires the current graph to
// still contain that exact edge; stale UI facts fail closed.
func (engine *Engine) Detail(ctx context.Context, request DetailRequest) (Detail, error) {
	request, err := normalizeDetailRequest(request)
	if err != nil {
		return Detail{}, err
	}
	invocation := engine.base
	invocation.Command = "context"
	invocation.Arguments = []string{request.Target}
	invocation.JSON = true
	invocation.Limit = request.Limit
	invocation.ContextLines = request.ContextLines
	invocation.MaxSourceBytes = request.MaxSourceBytes
	response, err := engine.app.Execute(ctx, invocation)
	if err != nil {
		return Detail{}, err
	}
	if response.Context == nil || response.Context.Schema != contextquery.Schema {
		return Detail{}, fmt.Errorf("context query returned no compatible source-rich result")
	}
	result := Detail{Schema: Schema, Kind: "node", Context: response.Context}
	if request.EdgeID == "" {
		return result, nil
	}
	for index := range response.Context.Outgoing {
		if response.Context.Outgoing[index].Edge.ID == request.EdgeID {
			relationship := response.Context.Outgoing[index]
			return Detail{Schema: Schema, Kind: "edge", Relationship: &relationship}, nil
		}
	}
	return Detail{}, fmt.Errorf("selected edge %q is no longer in the current bounded context; refresh the graph", request.EdgeID)
}

// Links returns authored declarations through the canonical application use
// case rather than reading the declaration file from the explorer.
func (engine *Engine) Links(ctx context.Context) (LinkList, error) {
	invocation := engine.base
	invocation.Command = "links list"
	invocation.Arguments = nil
	invocation.JSON = true
	response, err := engine.app.Execute(ctx, invocation)
	if err != nil {
		return LinkList{}, err
	}
	if response.LinkRevision == "" {
		return LinkList{}, fmt.Errorf("authored relationship list returned no revision")
	}
	return LinkList{Schema: Schema, Revision: response.LinkRevision, Links: response.Links}, nil
}

// MutateLink delegates one revision-guarded create, update, or remove to the
// same application primitive used by the CLI. Its result names the one
// affected declaration; callers reload Links when they need canonical state.
func (engine *Engine) MutateLink(ctx context.Context, operation string, request LinkMutation) (LinkMutationResult, error) {
	request, err := normalizeLinkMutation(operation, request)
	if err != nil {
		return LinkMutationResult{}, err
	}
	invocation := engine.base
	invocation.Command = "links " + operation
	invocation.Arguments = []string{request.ID}
	invocation.JSON = true
	invocation.LinkRevision = request.Revision
	if request.From != nil {
		invocation.LinkFrom, invocation.LinkFromSet = *request.From, true
	}
	if request.To != nil {
		invocation.LinkTo, invocation.LinkToSet = *request.To, true
	}
	if request.Kind != nil {
		invocation.LinkKind, invocation.LinkKindSet = *request.Kind, true
	}
	if request.Note != nil {
		invocation.LinkNote, invocation.LinkNoteSet = *request.Note, true
	}
	response, err := engine.app.Execute(ctx, invocation)
	if err != nil {
		return LinkMutationResult{}, err
	}
	if response.LinkRevision == "" {
		return LinkMutationResult{}, fmt.Errorf("authored relationship mutation returned no revision")
	}
	if len(response.Links) != 1 {
		return LinkMutationResult{}, fmt.Errorf("authored relationship mutation returned %d affected links, want 1", len(response.Links))
	}
	return LinkMutationResult{
		Schema: Schema, Operation: operation, Revision: response.LinkRevision, Link: response.Links[0],
	}, nil
}

// Diff returns the same stable-ID transition contract used by the CLI. The
// browser's keyed d3-graphviz renderer can consume its add/remove/change keys
// without a second graph or diff implementation.
func (engine *Engine) Diff(ctx context.Context, request DiffRequest) (graphdiff.Result, error) {
	request, err := normalizeDiffRequest(request)
	if err != nil {
		return graphdiff.Result{}, err
	}
	invocation := engine.base
	invocation.Command = "diff graph"
	invocation.Arguments = nil
	invocation.JSON = true
	invocation.DiffBase = request.Base
	invocation.DiffHead = request.Head
	invocation.MaxDepth = request.MaxDepth
	invocation.Limit = request.Limit
	invocation.MaxEdges = request.MaxEdges
	invocation.Kinds = append([]graph.EdgeKind(nil), request.Kinds...)
	response, err := engine.app.Execute(ctx, invocation)
	if err != nil {
		return graphdiff.Result{}, err
	}
	if response.Diff == nil || response.Diff.Schema != graphdiff.Schema {
		return graphdiff.Result{}, fmt.Errorf("graph diff returned no compatible transition contract")
	}
	return *response.Diff, nil
}

func normalizeDiffRequest(request DiffRequest) (DiffRequest, error) {
	request.Base = strings.TrimSpace(request.Base)
	request.Head = strings.TrimSpace(request.Head)
	if request.Base == "" || len(request.Base) > 4096 || hasUnsafeText(request.Base) {
		return DiffRequest{}, fmt.Errorf("base is required and must be at most 4096 characters without control characters")
	}
	if len(request.Head) > 4096 || hasUnsafeText(request.Head) {
		return DiffRequest{}, fmt.Errorf("head must be at most 4096 characters without control characters")
	}
	if request.MaxDepth == 0 {
		request.MaxDepth = 8
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.MaxEdges == 0 {
		request.MaxEdges = 10000
	}
	if request.MaxDepth < 1 || request.MaxDepth > 100 || request.Limit < 1 || request.Limit > 5000 || request.MaxEdges < 1 || request.MaxEdges > 20000 {
		return DiffRequest{}, fmt.Errorf("diff bounds exceed explorer limits")
	}
	if len(request.Kinds) > len(edgeKinds) {
		return DiffRequest{}, fmt.Errorf("too many edge kinds")
	}
	for _, kind := range request.Kinds {
		if !graph.IsEdgeKind(kind) {
			return DiffRequest{}, fmt.Errorf("unknown edge kind %q", kind)
		}
	}
	return request, nil
}

func normalizeRequest(request Request) (Request, error) {
	request.Target = strings.TrimSpace(request.Target)
	if request.Target == "" {
		return Request{}, fmt.Errorf("target is required")
	}
	if len(request.Target) > 4096 || hasUnsafeText(request.Target) {
		return Request{}, fmt.Errorf("target must be at most 4096 characters without control characters")
	}
	if request.Direction == "" {
		request.Direction = query.DirectionBoth
	}
	if request.Direction != query.DirectionIncoming && request.Direction != query.DirectionOutgoing && request.Direction != query.DirectionBoth {
		return Request{}, fmt.Errorf("direction must be incoming, outgoing, or both")
	}
	if request.MaxDepth == 0 {
		request.MaxDepth = 3
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.MaxEdges == 0 {
		request.MaxEdges = 400
	}
	if request.MaxDepth < 1 || request.MaxDepth > 100 {
		return Request{}, fmt.Errorf("max_depth must be between 1 and 100")
	}
	if request.Limit < 1 || request.Limit > 5000 {
		return Request{}, fmt.Errorf("limit must be between 1 and 5000")
	}
	if request.MaxEdges < 1 || request.MaxEdges > 20000 {
		return Request{}, fmt.Errorf("max_edges must be between 1 and 20000")
	}
	if len(request.Kinds) > len(edgeKinds) || len(request.Providers) > 64 || len(request.Evidence) > len(evidenceKinds) {
		return Request{}, fmt.Errorf("too many filter values")
	}
	for _, kind := range request.Kinds {
		if !graph.IsEdgeKind(kind) {
			return Request{}, fmt.Errorf("unknown edge kind %q", kind)
		}
	}
	for _, provider := range request.Providers {
		if provider == "" || len(provider) > 256 || hasUnsafeText(provider) {
			return Request{}, fmt.Errorf("provider filters must be nonempty and at most 256 characters")
		}
	}
	for _, evidence := range request.Evidence {
		if !graph.IsEvidence(evidence) {
			return Request{}, fmt.Errorf("unknown evidence %q", evidence)
		}
	}
	request.Kinds = sortedCompact(request.Kinds)
	request.Providers = sortedCompact(request.Providers)
	request.Evidence = sortedCompact(request.Evidence)
	return request, nil
}

func normalizeDetailRequest(request DetailRequest) (DetailRequest, error) {
	request.Target = strings.TrimSpace(request.Target)
	request.EdgeID = strings.TrimSpace(request.EdgeID)
	if request.Target == "" || len(request.Target) > 4096 || hasUnsafeText(request.Target) {
		return DetailRequest{}, fmt.Errorf("target is required and must be at most 4096 characters without control characters")
	}
	if len(request.EdgeID) > 4096 || hasUnsafeText(request.EdgeID) {
		return DetailRequest{}, fmt.Errorf("edge_id must be at most 4096 characters without control characters")
	}
	if request.Limit == 0 {
		request.Limit = 64
	}
	if request.ContextLines == 0 {
		request.ContextLines = 2
	}
	if request.MaxSourceBytes == 0 {
		request.MaxSourceBytes = 64 << 10
	}
	if request.Limit < 1 || request.Limit > 512 {
		return DetailRequest{}, fmt.Errorf("detail limit must be between 1 and 512")
	}
	if request.ContextLines < 0 || request.ContextLines > 100 {
		return DetailRequest{}, fmt.Errorf("context_lines must be between 0 and 100")
	}
	if request.MaxSourceBytes < 1 || request.MaxSourceBytes > 4<<20 {
		return DetailRequest{}, fmt.Errorf("max_source_bytes must be between 1 and 4194304")
	}
	return request, nil
}

func normalizeLinkMutation(operation string, request LinkMutation) (LinkMutation, error) {
	if operation != "add" && operation != "update" && operation != "remove" {
		return LinkMutation{}, fmt.Errorf("unknown link mutation %q", operation)
	}
	if strings.TrimSpace(request.ID) == "" || len(request.ID) > 256 || hasUnsafeText(request.ID) || !utf8.ValidString(request.ID) {
		return LinkMutation{}, fmt.Errorf("link id is required and must be valid text of at most 256 bytes")
	}
	if !validLinkRevision(request.Revision) {
		return LinkMutation{}, fmt.Errorf("a valid authored relationship revision is required")
	}
	for _, field := range []struct {
		name  string
		value *string
		limit int
	}{
		{"from", request.From, 4096}, {"to", request.To, 4096}, {"note", request.Note, 8 << 10},
	} {
		if field.value != nil && (!utf8.ValidString(*field.value) || len(*field.value) > field.limit || hasUnsafeText(*field.value)) {
			return LinkMutation{}, fmt.Errorf("%s is invalid or exceeds %d bytes", field.name, field.limit)
		}
	}
	if request.From != nil && strings.TrimSpace(*request.From) == "" {
		return LinkMutation{}, fmt.Errorf("from must not be empty")
	}
	if request.To != nil && strings.TrimSpace(*request.To) == "" {
		return LinkMutation{}, fmt.Errorf("to must not be empty")
	}
	if request.Kind != nil && !graph.IsEdgeKind(*request.Kind) {
		return LinkMutation{}, fmt.Errorf("unknown edge kind %q", *request.Kind)
	}
	switch operation {
	case "add":
		if request.From == nil || request.To == nil || request.Kind == nil {
			return LinkMutation{}, fmt.Errorf("add requires from, to, and kind")
		}
	case "update":
		if request.From == nil && request.To == nil && request.Kind == nil && request.Note == nil {
			return LinkMutation{}, fmt.Errorf("update requires at least one changed field")
		}
	case "remove":
		if request.From != nil || request.To != nil || request.Kind != nil || request.Note != nil {
			return LinkMutation{}, fmt.Errorf("remove accepts only id and revision")
		}
	}
	return request, nil
}

func validLinkRevision(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func hasUnsafeText(value string) bool {
	return strings.ContainsRune(value, '\x00') || strings.IndexFunc(value, func(current rune) bool {
		return unicode.IsControl(current) && current != '\t'
	}) >= 0
}

func filterResponse(response application.Response, request Request) application.Response {
	kinds := set(request.Kinds)
	providers := set(request.Providers)
	evidence := set(request.Evidence)
	filtered := make([]graph.Edge, 0, len(response.Edges))
	for _, edge := range response.Edges {
		if len(kinds) != 0 && !kinds[edge.Kind] {
			continue
		}
		if len(providers) != 0 && !providers[edge.Provider] {
			continue
		}
		if len(evidence) != 0 && !evidence[edge.Evidence] {
			continue
		}
		filtered = append(filtered, edge)
	}
	focus := response.Nodes[0]
	retained := reachableFromFocus(focus, filtered, request.Direction)
	response.Edges = make([]graph.Edge, 0, len(filtered))
	for _, edge := range filtered {
		if retained[edge.From] && retained[edge.To] {
			response.Edges = append(response.Edges, edge)
		}
	}
	nodes := []string{focus}
	for _, id := range response.Nodes[1:] {
		if retained[id] {
			nodes = append(nodes, id)
		}
	}
	response.Nodes = nodes
	symbols := make([]graph.Symbol, 0, len(response.Symbols))
	for _, symbol := range response.Symbols {
		if retained[symbol.ID] {
			symbols = append(symbols, symbol)
		}
	}
	response.Symbols = symbols
	return response
}

func reachableFromFocus(focus string, edges []graph.Edge, direction query.Direction) map[string]bool {
	adjacency := make(map[string][]string)
	for _, edge := range edges {
		if direction == query.DirectionOutgoing || direction == query.DirectionBoth {
			adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		}
		if direction == query.DirectionIncoming || direction == query.DirectionBoth {
			adjacency[edge.To] = append(adjacency[edge.To], edge.From)
		}
	}
	seen := map[string]bool{focus: true}
	queue := []string{focus}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return seen
}

func resultNodes(response application.Response, focus string) ([]Node, string) {
	byID := make(map[string]graph.Symbol, len(response.Symbols))
	for _, symbol := range response.Symbols {
		byID[symbol.ID] = symbol
	}
	result := make([]Node, 0, len(response.Nodes))
	focusLabel := focus
	for _, id := range response.Nodes {
		symbol, ok := byID[id]
		label := id
		if ok {
			label = symbol.DisplayName
			if label == "" {
				label = symbol.StableName
			}
		}
		if id == focus {
			focusLabel = label
		}
		result = append(result, Node{
			ID: id, SVGID: dot.NodeSVGID(id), Label: label,
			Kind: symbol.Kind, Provider: symbol.Provider, Evidence: symbol.Evidence,
		})
	}
	return result, focusLabel
}

func resultEdges(edges []graph.Edge) []Edge {
	grouped := make(map[string][]graph.Edge, len(edges))
	for _, edge := range edges {
		id := dot.EdgeSVGID(edge)
		grouped[id] = append(grouped[id], edge)
	}
	result := make([]Edge, 0, len(grouped))
	for id, facts := range grouped {
		slices.SortFunc(facts, graph.CompareEdges)
		result = append(result, Edge{SVGID: id, Facts: facts})
	}
	slices.SortFunc(result, func(a, b Edge) int { return strings.Compare(a.SVGID, b.SVGID) })
	return result
}

func resultOptions(edges []graph.Edge) Options {
	providers := map[string]bool{}
	for _, edge := range edges {
		if edge.Provider != "" {
			providers[edge.Provider] = true
		}
	}
	return Options{
		Kinds:     append([]graph.EdgeKind(nil), edgeKinds...),
		Providers: sortedKeys(providers),
		Evidence:  append([]graph.Evidence(nil), evidenceKinds...),
	}
}

func set[T comparable](values []T) map[T]bool {
	result := make(map[T]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func sortedCompact[T ~string](values []T) []T {
	result := append([]T(nil), values...)
	slices.Sort(result)
	return slices.Compact(result)
}

func sortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

var edgeKinds = []graph.EdgeKind{
	graph.EdgeCalls, graph.EdgeContains, graph.EdgeDefines, graph.EdgeDependsOn,
	graph.EdgeDocuments, graph.EdgeEmbeds, graph.EdgeExposes, graph.EdgeExtends,
	graph.EdgeGenerates, graph.EdgeHandles, graph.EdgeImplements, graph.EdgeImports,
	graph.EdgeInstantiates, graph.EdgeLinksTo, graph.EdgeMemberOf, graph.EdgeReads,
	graph.EdgeReferences, graph.EdgeResolvesTo, graph.EdgeTests, graph.EdgeWrites,
}

var evidenceKinds = []graph.Evidence{
	graph.EvidenceExact, graph.EvidenceDeclared, graph.EvidenceGenerated,
	graph.EvidenceInferred, graph.EvidenceSyntactic, graph.EvidenceAmbiguous,
}
