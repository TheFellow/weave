// Package application defines Weave's use cases independently of CLI parsing.
package application

import (
	"context"
	"fmt"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/storage"
)

const QuerySchema = "weave.query/v1"

// Invocation is a validated operation from a delivery surface.
type Invocation struct {
	Command   string
	Arguments []string
	JSON      bool
	Limit     int
	MaxDepth  int
	Kinds     []graph.EdgeKind
}

// Response is the stable application result consumed by text and JSON renderers.
type Response struct {
	Schema      string             `json:"schema"`
	Command     string             `json:"command"`
	Query       []string           `json:"query,omitempty"`
	Truncated   bool               `json:"truncated"`
	Symbols     []graph.Symbol     `json:"symbols,omitempty"`
	Occurrences []graph.Occurrence `json:"occurrences,omitempty"`
	Edges       []graph.Edge       `json:"edges,omitempty"`
	Nodes       []string           `json:"nodes,omitempty"`
	Export      *graph.Snapshot    `json:"export,omitempty"`
	Issues      []storage.Issue    `json:"issues,omitempty"`
}

// Service executes Weave use cases.
type Service interface {
	Execute(context.Context, Invocation) (Response, error)
}

// Noop explicitly implements unfinished commands without output.
type Noop struct{}

func (Noop) Execute(_ context.Context, invocation Invocation) (Response, error) {
	return Response{Schema: QuerySchema, Command: invocation.Command}, nil
}

// Local executes queries against one local database file. Each invocation owns
// the file handle, which permits gc to compact offline without hidden state.
type Local struct{ DatabasePath string }

// Execute runs one local use case.
func (app Local) Execute(ctx context.Context, invocation Invocation) (Response, error) {
	response := Response{Schema: QuerySchema, Command: invocation.Command, Query: append([]string(nil), invocation.Arguments...)}
	if invocation.Command == "gc" {
		return response, storage.Compact(ctx, app.DatabasePath)
	}
	if !requiresDatabase(invocation.Command) {
		return Noop{}.Execute(ctx, invocation)
	}
	db, err := storage.Open(ctx, app.DatabasePath, storage.Options{MustExist: true})
	if err != nil {
		return Response{}, err
	}
	defer db.Close()

	switch invocation.Command {
	case "symbols":
		response.Symbols, response.Truncated, err = db.FindSymbols(ctx, invocation.Arguments[0], invocation.Limit)
	case "definition":
		var symbols []graph.Symbol
		symbols, response.Truncated, err = db.FindSymbols(ctx, invocation.Arguments[0], invocation.Limit)
		for _, symbol := range symbols {
			if symbol.DocumentID != "" {
				response.Symbols = append(response.Symbols, symbol)
			}
		}
	case "references":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, db, invocation.Arguments[0]); err == nil {
			response.Occurrences, response.Truncated, err = db.Occurrences(ctx, symbol.ID, []string{"reference"}, invocation.Limit)
		}
	case "callers", "callees":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, db, invocation.Arguments[0]); err == nil {
			if invocation.Command == "callers" {
				response.Edges, response.Truncated, err = db.EdgesTo(ctx, symbol.ID, []graph.EdgeKind{graph.EdgeCalls}, invocation.Limit)
			} else {
				response.Edges, response.Truncated, err = db.EdgesFrom(ctx, symbol.ID, []graph.EdgeKind{graph.EdgeCalls}, invocation.Limit)
			}
		}
	case "path":
		var from, to graph.Symbol
		if from, err = query.Resolve(ctx, db, invocation.Arguments[0]); err != nil {
			break
		}
		if to, err = query.Resolve(ctx, db, invocation.Arguments[1]); err != nil {
			break
		}
		var traversal query.Traversal
		traversal, err = query.Path(ctx, db, from.ID, to.ID, invocation.Kinds, bounds(invocation))
		response.Truncated, response.Edges, response.Nodes = traversal.Truncated, traversal.Edges, traversal.Nodes
	case "impact":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, db, invocation.Arguments[0]); err != nil {
			break
		}
		kinds := invocation.Kinds
		if len(kinds) == 0 {
			kinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeDependsOn, graph.EdgeImplements, graph.EdgeImports, graph.EdgeTests}
		}
		var traversal query.Traversal
		traversal, err = query.Impact(ctx, db, symbol.ID, kinds, bounds(invocation))
		response.Truncated, response.Edges, response.Nodes = traversal.Truncated, traversal.Edges, traversal.Nodes
	case "export":
		var snapshot graph.Snapshot
		snapshot, err = db.Export(ctx)
		response.Export = &snapshot
	case "verify":
		response.Issues, err = db.Verify(ctx)
	default:
		return Noop{}.Execute(ctx, invocation)
	}
	if err != nil {
		return Response{}, fmt.Errorf("%s: %w", invocation.Command, err)
	}
	return response, nil
}

func requiresDatabase(command string) bool {
	switch command {
	case "symbols", "definition", "references", "callers", "callees", "path", "impact", "export", "verify":
		return true
	default:
		return false
	}
}

func bounds(invocation Invocation) query.Bounds {
	maxEdges := invocation.Limit * 100
	if maxEdges < 1000 {
		maxEdges = 1000
	}
	return query.Bounds{MaxDepth: invocation.MaxDepth, MaxNodes: invocation.Limit, MaxEdges: maxEdges}
}
