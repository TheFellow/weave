// Package query implements bounded deterministic semantic graph queries.
package query

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/TheFellow/weave/internal/graph"
)

var ErrNotFound = errors.New("symbol not found")

// Store is the narrow graph persistence boundary needed by query services.
type Store interface {
	FindSymbols(context.Context, string, int) ([]graph.Symbol, bool, error)
	Symbol(context.Context, string) (graph.Symbol, bool, error)
	EdgesFrom(context.Context, string, []graph.EdgeKind, int) ([]graph.Edge, bool, error)
	EdgesTo(context.Context, string, []graph.EdgeKind, int) ([]graph.Edge, bool, error)
}

// Bounds cap graph work and returned nodes.
type Bounds struct {
	MaxDepth int
	MaxNodes int
	MaxEdges int
}

// Traversal is an ordered graph result. Path uses Edges; impact uses Nodes in
// breadth-first discovery order. Truncated means a bound prevented completion.
type Traversal struct {
	Nodes     []string
	Edges     []graph.Edge
	Truncated bool
}

// Direction selects which side of a focused node a neighborhood traverses.
type Direction string

const (
	DirectionIncoming Direction = "incoming"
	DirectionOutgoing Direction = "outgoing"
	DirectionBoth     Direction = "both"
)

// Resolve returns the highest-ranked deterministic symbol match.
func Resolve(ctx context.Context, store Store, value string) (graph.Symbol, error) {
	if symbol, ok, err := store.Symbol(ctx, value); err != nil {
		return graph.Symbol{}, err
	} else if ok {
		return symbol, nil
	}
	symbols, _, err := store.FindSymbols(ctx, value, 2)
	if err != nil {
		return graph.Symbol{}, err
	}
	if len(symbols) == 0 {
		return graph.Symbol{}, fmt.Errorf("%w: %s", ErrNotFound, value)
	}
	return symbols[0], nil
}

// Path returns a shortest directed path using stable neighbor order.
func Path(ctx context.Context, store Store, from, to string, kinds []graph.EdgeKind, bounds Bounds) (Traversal, error) {
	if err := bounds.validate(); err != nil {
		return Traversal{}, err
	}
	if from == to {
		return Traversal{Nodes: []string{from}}, nil
	}
	type queued struct {
		id    string
		depth int
	}
	queue := []queued{{from, 0}}
	seen := map[string]bool{from: true}
	predecessor := map[string]graph.Edge{}
	examined := 0
	truncated := false
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= bounds.MaxDepth {
			truncated = true
			continue
		}
		remaining := bounds.MaxEdges - examined
		if remaining <= 0 {
			truncated = true
			break
		}
		edges, adjacencyTruncated, err := store.EdgesFrom(ctx, current.id, kinds, remaining)
		if err != nil {
			return Traversal{}, err
		}
		examined += len(edges)
		truncated = truncated || adjacencyTruncated
		for _, edge := range edges {
			if seen[edge.To] {
				continue
			}
			seen[edge.To] = true
			predecessor[edge.To] = edge
			if edge.To == to {
				return buildPath(from, to, predecessor, truncated), nil
			}
			if len(seen) >= bounds.MaxNodes {
				truncated = true
				continue
			}
			queue = append(queue, queued{edge.To, current.depth + 1})
		}
	}
	return Traversal{Truncated: truncated}, nil
}

func buildPath(from, to string, predecessor map[string]graph.Edge, truncated bool) Traversal {
	var reversed []graph.Edge
	for current := to; current != from; {
		edge := predecessor[current]
		reversed = append(reversed, edge)
		current = edge.From
	}
	slices.Reverse(reversed)
	nodes := []string{from}
	for _, edge := range reversed {
		nodes = append(nodes, edge.To)
	}
	return Traversal{Nodes: nodes, Edges: reversed, Truncated: truncated}
}

// Impact walks reverse adjacency in breadth-first discovery order.
func Impact(ctx context.Context, store Store, root string, kinds []graph.EdgeKind, bounds Bounds) (Traversal, error) {
	return ImpactMany(ctx, store, []string{root}, kinds, bounds)
}

// ImpactMany walks reverse adjacency from deterministic, de-duplicated roots.
func ImpactMany(ctx context.Context, store Store, roots []string, kinds []graph.EdgeKind, bounds Bounds) (Traversal, error) {
	if err := bounds.validate(); err != nil {
		return Traversal{}, err
	}
	roots = append([]string(nil), roots...)
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if len(roots) == 0 {
		return Traversal{}, errors.New("impact requires at least one graph root")
	}
	if len(roots) > bounds.MaxNodes {
		return Traversal{Nodes: roots[:bounds.MaxNodes], Truncated: true}, nil
	}
	type queued struct {
		id    string
		depth int
	}
	queue := make([]queued, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		queue = append(queue, queued{root, 0})
		seen[root] = true
	}
	result := Traversal{Nodes: append([]string(nil), roots...)}
	examined := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= bounds.MaxDepth {
			result.Truncated = true
			continue
		}
		remaining := bounds.MaxEdges - examined
		if remaining <= 0 {
			result.Truncated = true
			break
		}
		edges, truncated, err := store.EdgesTo(ctx, current.id, kinds, remaining)
		if err != nil {
			return Traversal{}, err
		}
		examined += len(edges)
		result.Truncated = result.Truncated || truncated
		for _, edge := range edges {
			if seen[edge.From] {
				continue
			}
			if len(result.Nodes) >= bounds.MaxNodes {
				result.Truncated = true
				continue
			}
			seen[edge.From] = true
			result.Nodes = append(result.Nodes, edge.From)
			result.Edges = append(result.Edges, edge)
			queue = append(queue, queued{edge.From, current.depth + 1})
		}
	}
	return result, nil
}

// Neighborhood returns a bounded focused subgraph. Incoming and outgoing
// walks keep independent visited sets so a node that is both an importer and a
// dependency remains reachable in both roles. The two queues are interleaved
// to avoid spending every shared bound on one direction first.
func Neighborhood(ctx context.Context, store Store, root string, kinds []graph.EdgeKind, direction Direction, bounds Bounds) (Traversal, error) {
	if err := bounds.validate(); err != nil {
		return Traversal{}, err
	}
	if root == "" {
		return Traversal{}, errors.New("neighborhood requires a graph root")
	}
	if direction != DirectionIncoming && direction != DirectionOutgoing && direction != DirectionBoth {
		return Traversal{}, fmt.Errorf("unknown neighborhood direction %q", direction)
	}
	type queued struct {
		id        string
		depth     int
		direction Direction
	}
	var queue []queued
	if direction == DirectionOutgoing || direction == DirectionBoth {
		queue = append(queue, queued{id: root, direction: DirectionOutgoing})
	}
	if direction == DirectionIncoming || direction == DirectionBoth {
		queue = append(queue, queued{id: root, direction: DirectionIncoming})
	}
	seenStates := make(map[string]bool, bounds.MaxNodes*2)
	for _, item := range queue {
		seenStates[string(item.direction)+"\x00"+item.id] = true
	}
	seenNodes := map[string]bool{root: true}
	result := Traversal{Nodes: []string{root}}
	seenEdges := make(map[string]bool, bounds.MaxEdges)
	examined := 0
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= bounds.MaxDepth {
			remaining := bounds.MaxEdges - examined
			if remaining <= 0 {
				result.Truncated = true
				continue
			}
			var edges []graph.Edge
			var truncated bool
			var err error
			if current.direction == DirectionOutgoing {
				edges, truncated, err = store.EdgesFrom(ctx, current.id, kinds, 1)
			} else {
				edges, truncated, err = store.EdgesTo(ctx, current.id, kinds, 1)
			}
			if err != nil {
				return Traversal{}, err
			}
			examined += len(edges)
			result.Truncated = result.Truncated || truncated || len(edges) != 0
			continue
		}
		remaining := bounds.MaxEdges - examined
		if remaining <= 0 {
			result.Truncated = true
			break
		}
		var edges []graph.Edge
		var truncated bool
		var err error
		if current.direction == DirectionOutgoing {
			edges, truncated, err = store.EdgesFrom(ctx, current.id, kinds, remaining)
		} else {
			edges, truncated, err = store.EdgesTo(ctx, current.id, kinds, remaining)
		}
		if err != nil {
			return Traversal{}, err
		}
		examined += len(edges)
		result.Truncated = result.Truncated || truncated
		for _, edge := range edges {
			next := edge.To
			if current.direction == DirectionIncoming {
				next = edge.From
			}
			if !seenNodes[next] {
				if len(seenNodes) >= bounds.MaxNodes {
					result.Truncated = true
					continue
				}
				seenNodes[next] = true
				result.Nodes = append(result.Nodes, next)
			}
			if !seenEdges[edge.ID] {
				seenEdges[edge.ID] = true
				result.Edges = append(result.Edges, edge)
			}
			state := string(current.direction) + "\x00" + next
			if !seenStates[state] {
				seenStates[state] = true
				queue = append(queue, queued{id: next, depth: current.depth + 1, direction: current.direction})
			}
		}
	}
	slices.SortFunc(result.Edges, graph.CompareEdges)
	return result, nil
}

func (b Bounds) validate() error {
	if b.MaxDepth <= 0 || b.MaxDepth > 100 {
		return fmt.Errorf("max depth must be between 1 and 100")
	}
	if b.MaxNodes <= 0 || b.MaxNodes > 100_000 {
		return fmt.Errorf("max nodes must be between 1 and 100000")
	}
	if b.MaxEdges <= 0 || b.MaxEdges > 1_000_000 {
		return fmt.Errorf("max edges must be between 1 and 1000000")
	}
	return nil
}
