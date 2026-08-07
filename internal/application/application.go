// Package application defines Weave's use cases independently of CLI parsing.
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/architecture"
	"github.com/TheFellow/weave/internal/buildinfo"
	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/ci"
	"github.com/TheFellow/weave/internal/federation"
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
	Command        string
	Arguments      []string
	JSON           bool
	Limit          int
	MaxDepth       int
	Kinds          []graph.EdgeKind
	SCIPPath       string
	AdapterPath    string
	AdapterArgs    []string
	Timeout        time.Duration
	Permissions    adapter.Permissions
	CatalogPath    string
	Scope          string
	Repositories   []string
	Format         string
	ConfigPath     string
	MaxRepos       int
	ImpactFiles    []string
	ImpactPackages []string
	DiffRevision   string
}

// Response is the stable application result consumed by text and JSON renderers.
type Response struct {
	Schema       string                 `json:"schema"`
	Command      string                 `json:"command"`
	Query        []string               `json:"query,omitempty"`
	Truncated    bool                   `json:"truncated"`
	Symbols      []graph.Symbol         `json:"symbols,omitempty"`
	Occurrences  []graph.Occurrence     `json:"occurrences,omitempty"`
	Edges        []graph.Edge           `json:"edges,omitempty"`
	Nodes        []string               `json:"nodes,omitempty"`
	Tests        []graph.Symbol         `json:"tests,omitempty"`
	Export       *graph.Snapshot        `json:"export,omitempty"`
	Issues       []storage.Issue        `json:"issues,omitempty"`
	Freshness    *freshness.Status      `json:"freshness,omitempty"`
	Diagnostics  []string               `json:"diagnostics,omitempty"`
	Adapters     []AdapterStatus        `json:"adapters,omitempty"`
	Repositories []catalog.Entry        `json:"repositories,omitempty"`
	Failed       bool                   `json:"failed,omitempty"`
	Sources      []federation.Source    `json:"sources,omitempty"`
	Architecture *architecture.Report   `json:"architecture,omitempty"`
	SARIF        *architecture.SARIFLog `json:"-"`
	CI           *ci.Status             `json:"ci,omitempty"`
	Version      *buildinfo.Info        `json:"version,omitempty"`
}

// AdapterStatus is an executable discovery result. List is side-effect free;
// doctor may run a bounded native describe handshake but never indexes,
// installs, restores, or builds.
type AdapterStatus struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Available  bool     `json:"available"`
	Checked    bool     `json:"checked,omitempty"`
	Compatible bool     `json:"compatible,omitempty"`
	Path       string   `json:"path,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Version    string   `json:"version,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Detail     string   `json:"detail,omitempty"`
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
	FreshnessFor  func(string) *freshness.Manager
	SCIPImporter  scipimport.Importer
	AdapterRunner adapter.Runner
	CatalogPath   string
}

// Execute runs one local use case.
func (app Local) Execute(ctx context.Context, invocation Invocation) (Response, error) {
	response := Response{Schema: QuerySchema, Command: invocation.Command, Query: append([]string(nil), invocation.Arguments...)}
	if invocation.Command == "version" {
		value := buildinfo.Read()
		response.Version = &value
		return response, nil
	}
	if strings.HasPrefix(invocation.Command, "repos ") {
		return app.repositories(ctx, response, invocation)
	}
	if strings.HasPrefix(invocation.Command, "ci ") {
		return app.ci(ctx, response, invocation)
	}
	if invocation.Command == "architecture check" {
		return app.architectureCheck(ctx, response, invocation)
	}
	if invocation.Scope == "catalog" && requiresDatabase(invocation.Command) {
		return app.federated(ctx, response, invocation)
	}
	if invocation.Command == "adapters list" || invocation.Command == "adapters doctor" {
		response.Adapters = inspectAdapters(ctx, invocation.Command == "adapters doctor", app.AdapterRunner)
		return response, nil
	}
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
		response.Symbols, response.Occurrences, response.Truncated, err = findDefinitions(ctx, db, invocation.Arguments[0], invocation.Limit)
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
	case "dependencies":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, db, invocation.Arguments[0]); err == nil {
			response.Edges, response.Truncated, err = db.EdgesFrom(ctx, symbol.ID, []graph.EdgeKind{graph.EdgeDependsOn, graph.EdgeImports}, invocation.Limit)
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
		kinds := invocation.Kinds
		if len(kinds) == 0 {
			kinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeDependsOn, graph.EdgeImplements, graph.EdgeImports, graph.EdgeTests}
		}
		var roots []string
		var snapshot graph.Snapshot
		if len(invocation.ImpactFiles) == 0 && len(invocation.ImpactPackages) == 0 && invocation.DiffRevision == "" {
			var symbol graph.Symbol
			if symbol, err = query.Resolve(ctx, db, invocation.Arguments[0]); err != nil {
				break
			}
			roots = []string{symbol.ID}
		} else {
			if snapshot, err = db.Export(ctx); err != nil {
				break
			}
			files := append([]string(nil), invocation.ImpactFiles...)
			if invocation.DiffRevision != "" {
				var repo repository.Repository
				if repo, err = app.repository(ctx); err != nil {
					break
				}
				var changed []string
				if changed, err = repo.DiffPaths(ctx, invocation.DiffRevision); err != nil {
					break
				}
				files = append(files, changed...)
			}
			roots, response.Diagnostics, err = impactRoots(snapshot, files, invocation.ImpactPackages)
			if err != nil {
				break
			}
		}
		var traversal query.Traversal
		traversal, err = query.ImpactMany(ctx, db, roots, kinds, bounds(invocation))
		response.Truncated, response.Edges, response.Nodes = traversal.Truncated, traversal.Edges, traversal.Nodes
		if err == nil && (len(invocation.ImpactFiles) != 0 || len(invocation.ImpactPackages) != 0 || invocation.DiffRevision != "") {
			if len(snapshot.Units) == 0 {
				snapshot, err = db.Export(ctx)
			}
			if err == nil {
				response.Tests = affectedTests(snapshot, traversal.Nodes)
			}
		}
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

func (app Local) ci(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	if app.Freshness == nil {
		return Response{}, fmt.Errorf("%s requires Git freshness management", invocation.Command)
	}
	if invocation.Command == "ci index" || invocation.Command == "ci check" {
		status, err := app.Freshness.Ensure(ctx, false)
		if err != nil {
			return Response{}, err
		}
		response.Freshness = &status
	}
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	state, err := repo.Inspect(ctx)
	if err != nil {
		return Response{}, err
	}
	configPath := invocation.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(repo.Root, ".weave", "architecture.json")
	} else if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(repo.Root, configPath)
	}
	status, err := ci.Key(repo, state, app.Freshness.ProviderID(), configPath)
	if err != nil {
		return Response{}, err
	}
	response.CI = &status
	if invocation.Command == "ci key" || invocation.Command == "ci index" {
		return response, nil
	}
	if invocation.Command != "ci check" {
		return Response{}, fmt.Errorf("unsupported CI command %q", invocation.Command)
	}
	databasePath, err := app.databasePath(ctx)
	if err != nil {
		return Response{}, err
	}
	db, err := storage.Open(ctx, databasePath, storage.Options{MustExist: true})
	if err != nil {
		return Response{}, err
	}
	response.Issues, err = db.Verify(ctx)
	closeErr := db.Close()
	if err != nil {
		return Response{}, err
	}
	if closeErr != nil {
		return Response{}, closeErr
	}
	response, err = app.architectureCheck(ctx, response, invocation)
	if err != nil {
		return Response{}, err
	}
	response.Failed = response.Failed || hasFatalIssues(response.Issues)
	if response.SARIF != nil {
		attachIntegritySARIF(response.SARIF, response.Issues)
	}
	return response, nil
}

func hasFatalIssues(issues []storage.Issue) bool {
	for _, issue := range issues {
		if issue.Fatal() {
			return true
		}
	}
	return false
}

func attachIntegritySARIF(log *architecture.SARIFLog, issues []storage.Issue) {
	if log == nil || len(issues) == 0 {
		return
	}
	if len(log.Runs) == 0 {
		log.Runs = []architecture.SARIFRun{{Tool: architecture.SARIFTool{Driver: architecture.SARIFDriver{Name: "weave", InformationURI: "https://github.com/TheFellow/weave"}}}}
	}
	driver := &log.Runs[0].Tool.Driver
	known := make(map[string]bool, len(driver.Rules))
	for _, rule := range driver.Rules {
		known[rule.ID] = true
	}
	invocation := architecture.SARIFInvocation{ExecutionSuccessful: true}
	for _, issue := range issues {
		ruleID := "weave/integrity/" + issue.Kind
		if !known[ruleID] {
			driver.Rules = append(driver.Rules, architecture.SARIFRule{ID: ruleID, ShortDescription: architecture.SARIFMessage{Text: "Weave graph integrity: " + issue.Kind}})
			known[ruleID] = true
		}
		level := "warning"
		if issue.Fatal() {
			level = "error"
			invocation.ExecutionSuccessful = false
		}
		invocation.ToolExecutionNotifications = append(invocation.ToolExecutionNotifications, architecture.SARIFNotification{
			Level:      level,
			Message:    architecture.SARIFMessage{Text: issue.Record + ": " + issue.Detail},
			Descriptor: architecture.SARIFDescriptorReference{ID: ruleID},
		})
	}
	log.Runs[0].Invocations = append(log.Runs[0].Invocations, invocation)
	slices.SortFunc(driver.Rules, func(a, b architecture.SARIFRule) int { return strings.Compare(a.ID, b.ID) })
}

func (app Local) architectureCheck(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	configPath := invocation.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(repo.Root, ".weave", "architecture.json")
	} else if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(repo.Root, configPath)
	}
	config, err := architecture.Load(configPath)
	if errors.Is(err, architecture.ErrNotConfigured) {
		report := architecture.Report{Schema: architecture.ReportSchema}
		response.Architecture = &report
		if invocation.Format == "sarif" {
			value := architecture.SARIF(architecture.Config{Schema: architecture.Schema}, report)
			response.SARIF = &value
		}
		return response, nil
	}
	if err != nil {
		return Response{}, err
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
	snapshot, err := db.Export(ctx)
	if err != nil {
		return Response{}, err
	}
	report := architecture.Check(config, snapshot)
	response.Architecture = &report
	response.Failed = len(report.Violations) != 0
	if invocation.Format == "sarif" {
		value := architecture.SARIF(config, report)
		response.SARIF = &value
	}
	return response, nil
}

func (app Local) federated(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	path, err := catalog.DefaultPath(firstNonempty(invocation.CatalogPath, app.CatalogPath))
	if err != nil {
		return Response{}, err
	}
	maxRepos := invocation.MaxRepos
	if maxRepos == 0 {
		maxRepos = 32
	}
	store, err := federation.OpenFresh(ctx, path, invocation.Repositories, maxRepos, func(ctx context.Context, root string) error {
		if app.FreshnessFor == nil {
			return errors.New("automatic freshness is unavailable in this application")
		}
		manager := app.FreshnessFor(root)
		if manager == nil {
			return errors.New("freshness manager is unavailable")
		}
		_, err := manager.Ensure(ctx, false)
		return err
	})
	if err != nil {
		return Response{}, err
	}
	defer store.Close()
	switch invocation.Command {
	case "symbols":
		response.Symbols, response.Truncated, err = store.FindSymbols(ctx, invocation.Arguments[0], invocation.Limit)
	case "definition":
		response.Symbols, response.Occurrences, response.Truncated, err = findDefinitions(ctx, store, invocation.Arguments[0], invocation.Limit)
	case "references":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, store, invocation.Arguments[0]); err == nil {
			response.Occurrences, response.Truncated, err = store.Occurrences(ctx, symbol.ID, []string{"reference"}, invocation.Limit)
		}
	case "callers", "callees":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, store, invocation.Arguments[0]); err == nil {
			if invocation.Command == "callers" {
				response.Edges, response.Truncated, err = store.EdgesTo(ctx, symbol.ID, []graph.EdgeKind{graph.EdgeCalls}, invocation.Limit)
			} else {
				response.Edges, response.Truncated, err = store.EdgesFrom(ctx, symbol.ID, []graph.EdgeKind{graph.EdgeCalls}, invocation.Limit)
			}
		}
	case "path":
		var from, to graph.Symbol
		if from, err = query.Resolve(ctx, store, invocation.Arguments[0]); err != nil {
			break
		}
		if to, err = query.Resolve(ctx, store, invocation.Arguments[1]); err != nil {
			break
		}
		var traversal query.Traversal
		traversal, err = query.Path(ctx, store, from.ID, to.ID, invocation.Kinds, bounds(invocation))
		response.Truncated, response.Edges, response.Nodes = traversal.Truncated, traversal.Edges, traversal.Nodes
	case "impact":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, store, invocation.Arguments[0]); err != nil {
			break
		}
		kinds := invocation.Kinds
		if len(kinds) == 0 {
			kinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeDependsOn, graph.EdgeImplements, graph.EdgeImports, graph.EdgeTests}
		}
		var traversal query.Traversal
		traversal, err = query.Impact(ctx, store, symbol.ID, kinds, bounds(invocation))
		response.Truncated, response.Edges, response.Nodes = traversal.Truncated, traversal.Edges, traversal.Nodes
	default:
		return Response{}, fmt.Errorf("%s is not a federated query", invocation.Command)
	}
	if err != nil {
		return Response{}, fmt.Errorf("%s: %w", invocation.Command, err)
	}
	response.Diagnostics = store.Diagnostics()
	response.Sources = store.Sources()
	return response, nil
}

type definitionStore interface {
	FindSymbols(context.Context, string, int) ([]graph.Symbol, bool, error)
	Occurrences(context.Context, string, []string, int) ([]graph.Occurrence, bool, error)
}

// findDefinitions returns every compiler-reported binding occurrence. The
// singular Symbol.Definition is retained only as a display anchor/fallback for
// older providers that do not emit definition occurrences.
func findDefinitions(ctx context.Context, store definitionStore, value string, limit int) ([]graph.Symbol, []graph.Occurrence, bool, error) {
	matches, truncated, err := store.FindSymbols(ctx, value, limit)
	if err != nil {
		return nil, nil, false, err
	}
	var anchors []graph.Symbol
	var definitions []graph.Occurrence
	for _, symbol := range matches {
		remaining := limit - len(anchors) - len(definitions)
		if remaining <= 0 {
			return anchors, definitions, true, nil
		}
		occurrences, occurrenceTruncated, err := store.Occurrences(ctx, symbol.ID, []string{"definition"}, remaining)
		if err != nil {
			return nil, nil, false, err
		}
		truncated = truncated || occurrenceTruncated
		if len(occurrences) == 0 {
			if symbol.DocumentID != "" {
				anchors = append(anchors, symbol)
			}
			continue
		}
		definitions = append(definitions, occurrences...)
	}
	return anchors, definitions, truncated, nil
}

func (app Local) repositories(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	path, err := catalog.DefaultPath(firstNonempty(invocation.CatalogPath, app.CatalogPath))
	if err != nil {
		return Response{}, err
	}
	db, err := catalog.Open(ctx, path, invocation.Timeout)
	if err != nil {
		return Response{}, err
	}
	defer db.Close()
	switch invocation.Command {
	case "repos add":
		directory := "."
		if len(invocation.Arguments) == 1 {
			directory = invocation.Arguments[0]
		}
		entry, err := db.Add(ctx, directory)
		if invocation.JSON {
			response.Repositories = []catalog.Entry{entry}
		}
		return response, err
	case "repos remove":
		_, err := db.Remove(ctx, invocation.Arguments[0])
		return response, err
	case "repos list", "repos status":
		response.Repositories, err = db.List(ctx)
		return response, err
	case "repos sync":
		response.Repositories, response.Diagnostics, err = db.Sync(ctx, invocation.Arguments)
		if !invocation.JSON {
			response.Repositories = nil
		}
		return response, err
	default:
		return Response{}, fmt.Errorf("unsupported repository command %q", invocation.Command)
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func inspectAdapters(ctx context.Context, doctor bool, runner adapter.Runner) []AdapterStatus {
	type candidate struct{ name, kind, configured string }
	candidates := []candidate{
		{"weave-dotnet", "native", os.Getenv("WEAVE_DOTNET_ADAPTER")},
		{"weave-python", "native", os.Getenv("WEAVE_PYTHON_ADAPTER")},
		{"scip-dotnet", "scip-producer", os.Getenv("WEAVE_SCIP_DOTNET")},
	}
	if doctor {
		candidates = append(candidates, candidate{"dotnet", "runtime", ""})
	}
	statuses := make([]AdapterStatus, 0, len(candidates))
	for _, candidate := range candidates {
		value := candidate.name
		detail := "not found on PATH"
		if candidate.configured != "" {
			value = candidate.configured
			detail = "configured by environment"
		}
		path, err := resolveExecutable(value)
		status := AdapterStatus{Name: candidate.name, Kind: candidate.kind, Detail: detail}
		if err == nil {
			status.Available, status.Path = true, path
			if candidate.configured == "" {
				status.Detail = "discovered on PATH"
			}
		} else if candidate.configured != "" {
			status.Detail = "configured path unavailable: " + err.Error()
		}
		if doctor && status.Available && candidate.kind == "native" {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			capabilities, stderr, probeErr := runner.Describe(probeCtx, adapter.Executable{Path: path, Env: adapterEnvironment()})
			cancel()
			status.Checked = true
			if probeErr != nil {
				status.Detail = "protocol check failed: " + probeErr.Error()
				if stderr != "" {
					status.Detail += ": " + stderr
				}
			} else {
				status.Compatible = true
				status.Provider, status.Version = capabilities.Provider.Name, capabilities.Provider.Version
				status.Languages = append([]string(nil), capabilities.Languages...)
				slices.Sort(status.Languages)
				status.Detail = "compatible with " + adapter.Protocol
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (app Local) importSCIP(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	repo, err := app.repository(ctx)
	if err != nil {
		return Response{}, err
	}
	result, err := app.SCIPImporter.ImportFile(ctx, invocation.SCIPPath, scipimport.Options{
		RepositoryRoot: repo.Root, RepositoryIdentity: repo.Identity,
	})
	if err != nil {
		return Response{}, err
	}
	if err := app.publish(ctx, result.Units, func(unit graph.Unit) bool { return unit.Provider == result.Provider }); err != nil {
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
		path, err := exec.LookPath(absolute)
		if err != nil {
			return "", fmt.Errorf("find adapter executable %q: %w", value, err)
		}
		return path, nil
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
	case "symbols", "definition", "references", "callers", "callees", "dependencies", "path", "impact", "export", "verify":
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
