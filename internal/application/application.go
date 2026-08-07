// Package application defines Weave's use cases independently of CLI parsing.
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/scipimport"
	"github.com/TheFellow/weave/internal/storage"
)

const QuerySchema = "weave.query/v1"

// Invocation is a validated operation from a delivery surface.
type Invocation struct {
	Command     string
	Arguments   []string
	JSON        bool
	Limit       int
	MaxDepth    int
	Kinds       []graph.EdgeKind
	SCIPPath    string
	AdapterPath string
	AdapterArgs []string
	Timeout     time.Duration
	Permissions adapter.Permissions
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
	Freshness   *freshness.Status  `json:"freshness,omitempty"`
	Diagnostics []string           `json:"diagnostics,omitempty"`
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
type Local struct {
	DatabasePath  string
	Directory     string
	Freshness     *freshness.Manager
	SCIPImporter  scipimport.Importer
	AdapterRunner adapter.Runner
}

// Execute runs one local use case.
func (app Local) Execute(ctx context.Context, invocation Invocation) (Response, error) {
	response := Response{Schema: QuerySchema, Command: invocation.Command, Query: append([]string(nil), invocation.Arguments...)}
	if invocation.Command == "index" && invocation.SCIPPath != "" {
		return app.importSCIP(ctx, response, invocation)
	}
	if invocation.Command == "index" && invocation.AdapterPath != "" {
		return app.indexAdapter(ctx, response, invocation)
	}
	if app.Freshness != nil {
		switch invocation.Command {
		case "init", "index":
			status, err := app.Freshness.Ensure(ctx, invocation.Command == "index")
			response.Freshness = &status
			return response, err
		case "status":
			status, err := app.Freshness.Inspect(ctx)
			response.Freshness = &status
			return response, err
		}
	}
	if invocation.Command == "gc" {
		path, err := app.databasePath(ctx)
		if err != nil {
			return Response{}, err
		}
		return response, storage.Compact(ctx, path)
	}
	if !requiresDatabase(invocation.Command) {
		return Noop{}.Execute(ctx, invocation)
	}
	if app.Freshness != nil {
		status, err := app.Freshness.Ensure(ctx, false)
		if err != nil {
			return Response{}, err
		}
		response.Freshness = &status
	}
	databasePath, err := app.databasePath(ctx)
	if err != nil {
		return Response{}, err
	}
	db, err := storage.Open(ctx, databasePath, storage.Options{MustExist: true})
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

func (app Local) importSCIP(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	units, err := app.SCIPImporter.ImportFile(ctx, invocation.SCIPPath, scipimport.Options{
		RepositoryRoot: repo.Root, RepositoryIdentity: repo.Identity,
	})
	if err != nil {
		return Response{}, err
	}
	if err := app.publish(ctx, units, func(unit graph.Unit) bool { return strings.HasPrefix(unit.Provider, "scip:") }); err != nil {
		return Response{}, err
	}
	return response, nil
}

func (app Local) indexAdapter(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	executable, err := resolveExecutable(invocation.AdapterPath)
	if err != nil {
		return Response{}, err
	}
	timeout := invocation.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	requestID, err := randomID()
	if err != nil {
		return Response{}, err
	}
	result, err := app.AdapterRunner.Index(runCtx, adapter.Executable{
		Path: executable, Args: invocation.AdapterArgs, Dir: repo.Root, Env: adapterEnvironment(),
	}, adapter.IndexRequest{
		RequestID: requestID, RepositoryRoot: repo.Root, RepositoryIdentity: repo.Identity,
		Permissions: invocation.Permissions,
	})
	if err != nil {
		return Response{}, err
	}
	if err := app.publish(ctx, result.Units, func(unit graph.Unit) bool { return unit.Provider == result.Provider.Name }); err != nil {
		return Response{}, err
	}
	for _, diagnostic := range result.Diagnostics {
		response.Diagnostics = append(response.Diagnostics, diagnostic.Severity+": "+diagnostic.Message)
	}
	if result.Stderr != "" {
		response.Diagnostics = append(response.Diagnostics, result.Stderr)
	}
	return response, nil
}

func (app Local) publish(ctx context.Context, units []graph.UnitFacts, owns func(graph.Unit) bool) error {
	path, err := app.databasePath(ctx)
	if err != nil {
		return err
	}
	db, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		return err
	}
	defer db.Close()
	snapshot, err := db.Export(ctx)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(units))
	for _, facts := range units {
		present[facts.Unit.ID] = true
	}
	var removed []string
	for _, unit := range snapshot.Units {
		if owns(unit) && !present[unit.ID] {
			removed = append(removed, unit.ID)
		}
	}
	slices.Sort(removed)
	return db.ReplaceUnits(ctx, units, removed)
}

func (app Local) repository(ctx context.Context) (repository.Repository, error) {
	directory := app.Directory
	if app.Freshness != nil {
		directory = app.Freshness.Directory
	}
	if directory == "" {
		directory = "."
	}
	return repository.Discover(ctx, directory)
}

func resolveExecutable(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("adapter executable is empty")
	}
	if strings.ContainsAny(value, `/\\`) {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("resolve adapter executable: %w", err)
		}
		return absolute, nil
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("find adapter executable %q: %w", value, err)
	}
	return path, nil
}

func adapterEnvironment() []string {
	allowed := []string{"PATH", "TMPDIR", "TMP", "TEMP", "SystemRoot", "WINDIR"}
	var environment []string
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create adapter request ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func (app Local) databasePath(ctx context.Context) (string, error) {
	if app.Freshness != nil {
		return app.Freshness.DatabasePath(ctx)
	}
	if app.DatabasePath == "" {
		return "", fmt.Errorf("database path is not configured")
	}
	return app.DatabasePath, nil
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
