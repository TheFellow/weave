package application

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
)

type workspaceStore interface {
	FindSymbols(context.Context, string, int) ([]graph.Symbol, bool, error)
	Symbol(context.Context, string) (graph.Symbol, bool, error)
	EdgesFrom(context.Context, string, []graph.EdgeKind, int) ([]graph.Edge, bool, error)
	EdgesTo(context.Context, string, []graph.EdgeKind, int) ([]graph.Edge, bool, error)
}

var workspaceKinds = map[string]bool{
	"workspace": true, "directory": true, "file": true, "symlink": true, "resource": true,
	"document": true, "section": true, "code-block": true, "asset": true,
	"route": true, "series": true, "topic": true, "url": true,
}

var contentEdgeKinds = []graph.EdgeKind{
	graph.EdgeLinksTo, graph.EdgeEmbeds, graph.EdgeDocuments, graph.EdgeGenerates, graph.EdgeExposes, graph.EdgeMemberOf, graph.EdgeResolvesTo,
}

func executeWorkspace(ctx context.Context, store workspaceStore, response *Response, invocation Invocation) error {
	switch invocation.Command {
	case "workspace find":
		values, truncated, err := findWorkspaceSymbols(ctx, store, invocation.Arguments[0], invocation.Limit)
		response.Symbols, response.Truncated = values, truncated
		return err
	case "workspace outline":
		root, err := resolveWorkspace(ctx, store, invocation.Arguments[0])
		if err != nil {
			return err
		}
		response.Symbols, response.Edges, response.Truncated, err = workspaceOutline(ctx, store, root, invocation.Limit, invocation.MaxDepth)
		return err
	case "workspace links":
		root, err := resolveWorkspace(ctx, store, invocation.Arguments[0])
		if err != nil {
			return err
		}
		response.Symbols, response.Edges, response.Truncated, err = workspaceLinks(ctx, store, root, invocation.Limit, invocation.MaxDepth)
		return err
	case "workspace backlinks":
		root, err := resolveWorkspace(ctx, store, invocation.Arguments[0])
		if err != nil {
			return err
		}
		response.Symbols, response.Edges, response.Truncated, err = workspaceBacklinks(ctx, store, root, invocation.Limit, invocation.MaxDepth)
		return err
	default:
		return fmt.Errorf("unsupported workspace command %q", invocation.Command)
	}
}

func findWorkspaceSymbols(ctx context.Context, store workspaceStore, value string, limit int) ([]graph.Symbol, bool, error) {
	candidateLimit := max(1024, limit*16)
	candidateLimit = min(candidateLimit, 100000)
	values, truncated, err := store.FindSymbols(ctx, value, candidateLimit)
	if err != nil {
		return nil, false, err
	}
	result := values[:0]
	for _, symbol := range values {
		if workspaceKinds[symbol.Kind] {
			result = append(result, symbol)
		}
	}
	if len(result) > limit {
		result, truncated = result[:limit], true
	}
	return result, truncated, nil
}

func resolveWorkspace(ctx context.Context, store workspaceStore, value string) (graph.Symbol, error) {
	if symbol, ok, err := store.Symbol(ctx, value); err != nil {
		return graph.Symbol{}, err
	} else if ok && workspaceKinds[symbol.Kind] {
		return symbol, nil
	}
	values, _, err := findWorkspaceSymbols(ctx, store, value, 64)
	if err != nil {
		return graph.Symbol{}, err
	}
	var exact []graph.Symbol
	for _, symbol := range values {
		if symbol.StableName == value {
			exact = append(exact, symbol)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		return graph.Symbol{}, fmt.Errorf("workspace target %q is ambiguous; use a stable symbol ID", value)
	}
	if len(values) == 0 {
		return graph.Symbol{}, fmt.Errorf("workspace target not found: %s", value)
	}
	if len(values) > 1 {
		return graph.Symbol{}, fmt.Errorf("workspace target %q is ambiguous; use a path, path#section, or stable symbol ID", value)
	}
	return values[0], nil
}

func workspaceOutline(ctx context.Context, store workspaceStore, root graph.Symbol, limit, maxDepth int) ([]graph.Symbol, []graph.Edge, bool, error) {
	if maxDepth <= 0 {
		maxDepth = 8
	}
	type queued struct {
		id    string
		depth int
	}
	queue := []queued{{root.ID, 0}}
	seen := map[string]bool{root.ID: true}
	symbols := []graph.Symbol{root}
	var edges []graph.Edge
	truncated := false
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= maxDepth {
			children, more, err := store.EdgesFrom(ctx, current.id, []graph.EdgeKind{graph.EdgeContains}, 1)
			if err != nil {
				return nil, nil, false, err
			}
			truncated = truncated || more || len(children) != 0
			continue
		}
		remaining := limit - len(symbols)
		if remaining <= 0 {
			truncated = true
			break
		}
		values, more, err := store.EdgesFrom(ctx, current.id, []graph.EdgeKind{graph.EdgeContains}, remaining)
		if err != nil {
			return nil, nil, false, err
		}
		truncated = truncated || more
		for _, edge := range values {
			if seen[edge.To] {
				continue
			}
			symbol, ok, err := store.Symbol(ctx, edge.To)
			if err != nil {
				return nil, nil, false, err
			}
			if !ok {
				continue
			}
			seen[edge.To] = true
			edges = append(edges, edge)
			symbols = append(symbols, symbol)
			queue = append(queue, queued{edge.To, current.depth + 1})
		}
	}
	return symbols, edges, truncated, nil
}

func workspaceBacklinks(ctx context.Context, store workspaceStore, root graph.Symbol, limit, maxDepth int) ([]graph.Symbol, []graph.Edge, bool, error) {
	targets, _, descendantsTruncated, err := workspaceOutline(ctx, store, root, limit, maxDepth)
	if err != nil {
		return nil, nil, false, err
	}
	byID := map[string]graph.Edge{}
	truncated := descendantsTruncated
	for _, target := range targets {
		remaining := limit - len(byID)
		if remaining <= 0 {
			truncated = true
			break
		}
		incoming, more, err := store.EdgesTo(ctx, target.ID, contentEdgeKinds, remaining)
		if err != nil {
			return nil, nil, false, err
		}
		truncated = truncated || more
		for _, edge := range incoming {
			byID[edge.ID] = edge
			if edge.Kind != graph.EdgeResolvesTo {
				continue
			}
			remaining = limit - len(byID)
			if remaining <= 0 {
				truncated = true
				break
			}
			links, linksMore, err := store.EdgesTo(ctx, edge.From, []graph.EdgeKind{graph.EdgeLinksTo, graph.EdgeEmbeds}, remaining)
			if err != nil {
				return nil, nil, false, err
			}
			truncated = truncated || linksMore
			for _, link := range links {
				byID[link.ID] = link
			}
		}
	}
	edges := make([]graph.Edge, 0, len(byID))
	for _, edge := range byID {
		edges = append(edges, edge)
	}
	slices.SortFunc(edges, graph.CompareEdges)
	if len(edges) > limit {
		edges, truncated = edges[:limit], true
	}
	symbols, err := edgeSymbols(ctx, store, edges, false)
	return symbols, edges, truncated, err
}

func workspaceLinks(ctx context.Context, store workspaceStore, root graph.Symbol, limit, maxDepth int) ([]graph.Symbol, []graph.Edge, bool, error) {
	nodes, _, descendantsTruncated, err := workspaceOutline(ctx, store, root, limit, maxDepth)
	if err != nil {
		return nil, nil, false, err
	}
	var edges []graph.Edge
	truncated := descendantsTruncated
	for _, node := range nodes {
		remaining := limit - len(edges)
		if remaining <= 0 {
			truncated = true
			break
		}
		values, more, err := store.EdgesFrom(ctx, node.ID, contentEdgeKinds, remaining)
		if err != nil {
			return nil, nil, false, err
		}
		edges = append(edges, values...)
		truncated = truncated || more
	}
	slices.SortFunc(edges, graph.CompareEdges)
	symbols, err := edgeSymbols(ctx, store, edges, true)
	return symbols, edges, truncated, err
}

func edgeSymbols(ctx context.Context, store workspaceStore, edges []graph.Edge, targets bool) ([]graph.Symbol, error) {
	byID := map[string]graph.Symbol{}
	for _, edge := range edges {
		id := edge.From
		if targets {
			id = edge.To
		}
		symbol, ok, err := store.Symbol(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			byID[id] = symbol
		}
	}
	result := make([]graph.Symbol, 0, len(byID))
	for _, symbol := range byID {
		result = append(result, symbol)
	}
	slices.SortFunc(result, func(a, b graph.Symbol) int {
		return strings.Compare(a.StableName+"\x00"+a.ID, b.StableName+"\x00"+b.ID)
	})
	return result, nil
}
