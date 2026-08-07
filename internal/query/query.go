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
	if err := bounds.validate(); err != nil {
		return Traversal{}, err
	}
	type queued struct {
		id    string
		depth int
	}
	queue := []queued{{root, 0}}
	seen := map[string]bool{root: true}
	result := Traversal{Nodes: []string{root}}
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
