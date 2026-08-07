// Package explorer serves a human-only local view over Weave's existing graph
// application query and deterministic DOT renderer.
package explorer

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/dot"
	"github.com/TheFellow/weave/internal/graph"
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

// Node maps a stable SVG element back to its exact semantic graph target.
type Node struct {
	ID       string         `json:"id"`
	SVGID    string         `json:"svg_id"`
	Label    string         `json:"label"`
	Kind     string         `json:"kind,omitempty"`
	Provider string         `json:"provider,omitempty"`
	Evidence graph.Evidence `json:"evidence,omitempty"`
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
	Options     Options  `json:"options"`
	Truncated   bool     `json:"truncated"`
	Diagnostics []string `json:"diagnostics,omitempty"`
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
		DOT: rendered.String(), Nodes: nodes, Options: options,
		Truncated: response.Truncated, Diagnostics: response.Diagnostics,
	}, nil
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
