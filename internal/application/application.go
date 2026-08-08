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
	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/buildinfo"
	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/ci"
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/federation"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/repository"
	"github.com/TheFellow/weave/internal/scipimport"
	"github.com/TheFellow/weave/internal/storage"
	"github.com/TheFellow/weave/internal/watch"
)

const QuerySchema = "weave.query/v1"

// Native runtimes can have cold-start latency under concurrent CI load. Keep
// doctor bounded without treating a healthy adapter as absent after five
// seconds of scheduler or runtime startup delay.
const adapterDoctorTimeout = 15 * time.Second

// Invocation is a validated operation from a delivery surface.
type Invocation struct {
	Command           string
	Arguments         []string
	JSON              bool
	Limit             int
	MaxDepth          int
	MaxEdges          int
	Kinds             []graph.EdgeKind
	Direction         query.Direction
	SCIPPath          string
	AdapterPath       string
	AdapterArgs       []string
	Timeout           time.Duration
	Permissions       adapter.Permissions
	CatalogPath       string
	Scope             string
	Repositories      []string
	Format            string
	ConfigPath        string
	MaxRepos          int
	ContextLines      int
	MaxSourceBytes    int
	ContextLimit      int
	ImpactFiles       []string
	ImpactPackages    []string
	DiffRevision      string
	DiffBase          string
	DiffHead          string
	LinkFrom          string
	LinkTo            string
	LinkNote          string
	LinkKind          graph.EdgeKind
	LinkFromSet       bool
	LinkToSet         bool
	LinkNoteSet       bool
	LinkKindSet       bool
	LinkRevision      string
	AdapterName       string
	AdapterSource     string
	AdapterArgsSet    bool
	AdapterPolicySet  bool
	AdapterTimeoutSet bool
}

// Response is the stable application result consumed by text and JSON renderers.
type Response struct {
	Schema       string                 `json:"schema"`
	Command      string                 `json:"command"`
	Query        []string               `json:"query,omitempty"`
	Truncated    bool                   `json:"truncated"`
	Symbols      []graph.Symbol         `json:"symbols,omitempty"`
	Documents    []graph.Document       `json:"documents,omitempty"`
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
	Links        []bridge.Link          `json:"links,omitempty"`
	LinkRevision string                 `json:"link_revision,omitempty"`
	Context      *contextquery.Result   `json:"context,omitempty"`
	Contexts     []contextquery.Result  `json:"contexts,omitempty"`
	Diff         *graphdiff.Result      `json:"diff,omitempty"`
}

// AdapterStatus is an executable discovery result. List is side-effect free;
// doctor may run a bounded native describe handshake but never indexes,
// installs, restores, or builds.
type AdapterStatus struct {
	Name              string                     `json:"name"`
	Kind              string                     `json:"kind"`
	Available         bool                       `json:"available"`
	Checked           bool                       `json:"checked,omitempty"`
	Compatible        bool                       `json:"compatible,omitempty"`
	Path              string                     `json:"path,omitempty"`
	Provider          string                     `json:"provider,omitempty"`
	Version           string                     `json:"version,omitempty"`
	Languages         []string                   `json:"languages,omitempty"`
	Detail            string                     `json:"detail,omitempty"`
	Source            string                     `json:"source,omitempty"`
	Integrity         string                     `json:"integrity,omitempty"`
	Claims            *adapter.Claims            `json:"claims,omitempty"`
	Requires          *adapter.Requirements      `json:"requires,omitempty"`
	CapabilityDigest  string                     `json:"capability_digest,omitempty"`
	RequirementStatus []AdapterRequirementStatus `json:"requirement_status,omitempty"`
	Permissions       adapter.Permissions        `json:"permissions,omitempty"`
	Activation        string                     `json:"activation,omitempty"`
}

type AdapterRequirementStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
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
	DatabasePath       string
	Directory          string
	Freshness          *freshness.Manager
	FreshnessFor       func(string) *freshness.Manager
	SCIPImporter       scipimport.Importer
	AdapterRunner      adapter.Runner
	Adapters           []adapter.Registration
	AdapterConfigError error
	AdapterStore       adapter.Store
	CatalogPath        string
}

// Watch warms the same query-authoritative per-worktree freshness coordinator.
// It owns no database, provider pipeline, or filesystem inventory of its own.
func (app Local) Watch(ctx context.Context, options watch.Options, sink watch.Sink) error {
	if app.Freshness == nil {
		return errors.New("watch requires repository-managed freshness; an explicit WEAVE_DATABASE is not watchable")
	}
	return watch.Run(ctx, app.Freshness, options, sink)
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
	if strings.HasPrefix(invocation.Command, "links ") {
		return app.links(ctx, response, invocation)
	}
	if strings.HasPrefix(invocation.Command, "ci ") {
		return app.ci(ctx, response, invocation)
	}
	if invocation.Command == "architecture check" {
		return app.architectureCheck(ctx, response, invocation)
	}
	if strings.HasPrefix(invocation.Command, "diff ") {
		return app.snapshotDiff(ctx, response, invocation)
	}
	if invocation.Scope == "catalog" && requiresDatabase(invocation.Command) {
		return app.federated(ctx, response, invocation)
	}
	if invocation.Command == "adapters list" || invocation.Command == "adapters doctor" {
		directory := app.Directory
		if app.Freshness != nil {
			directory = app.Freshness.Directory
		}
		if directory == "" {
			directory = "."
		}
		response.Adapters = inspectAdaptersWithErrorAt(ctx, invocation.Command == "adapters doctor", app.AdapterRunner, app.AdapterConfigError, directory, app.Adapters...)
		return response, nil
	}
	if strings.HasPrefix(invocation.Command, "adapters ") {
		return app.manageAdapter(ctx, response, invocation)
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
	case "context":
		var repo repository.Repository
		if repo, err = app.repository(ctx); err == nil {
			var result contextquery.Result
			result, err = contextquery.Build(ctx, db, invocation.Arguments[0], contextOptions(invocation), localLocator(repo))
			if err == nil {
				result.Metadata.Freshness.Checked = response.Freshness != nil
				result.Metadata.Freshness.Current = response.Freshness != nil && response.Freshness.Current
				response.Context = &result
				response.Truncated = contextTruncated(result.Metadata.Truncation)
			}
		}
	case "explore":
		var repo repository.Repository
		if repo, err = app.repository(ctx); err == nil {
			response.Contexts, response.Truncated, err = contextquery.Explore(ctx, db, invocation.Arguments[0], exploreOptions(invocation), localLocator(repo))
			if err == nil {
				for index := range response.Contexts {
					response.Contexts[index].Metadata.Freshness.Checked = response.Freshness != nil
					response.Contexts[index].Metadata.Freshness.Current = response.Freshness != nil && response.Freshness.Current
					response.Truncated = response.Truncated || contextTruncated(response.Contexts[index].Metadata.Truncation)
				}
			}
		}
	case "graph":
		err = executeGraph(ctx, db, &response, invocation)
	case "workspace find", "workspace outline", "workspace links", "workspace backlinks":
		err = executeWorkspace(ctx, db, &response, invocation)
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
			response.Edges, response.Truncated, err = directDependencies(ctx, db, symbol.ID, invocation.Limit)
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
			kinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeDependsOn, graph.EdgeImplements, graph.EdgeImports, graph.EdgeTests, graph.EdgeLinksTo, graph.EdgeEmbeds}
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
	if !strings.HasPrefix(invocation.Command, "workspace ") {
		if err := enrichResponse(ctx, db, &response); err != nil {
			return Response{}, fmt.Errorf("%s: enrich result: %w", invocation.Command, err)
		}
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
	var freshnessDiagnostics []string
	refresh := func(ctx context.Context, root string) (string, error) {
		if app.FreshnessFor == nil {
			return "", errors.New("automatic freshness is unavailable in this application")
		}
		manager := app.FreshnessFor(root)
		if manager == nil {
			return "", errors.New("freshness manager is unavailable")
		}
		status, err := manager.Ensure(ctx, false)
		for _, diagnostic := range status.Diagnostics {
			freshnessDiagnostics = append(freshnessDiagnostics, status.RepositoryIdentity+": "+diagnostic)
		}
		return status.Generation, err
	}
	var store *federation.Store
	if aggregateEligible(invocation.Command) {
		aggregateDirectory, pathErr := catalog.DefaultAggregateDirectory(path, "")
		if pathErr != nil {
			return Response{}, pathErr
		}
		store, err = federation.OpenFreshAccelerated(ctx, path, invocation.Repositories, maxRepos, aggregateDirectory, refresh)
	} else {
		store, err = federation.OpenFresh(ctx, path, invocation.Repositories, maxRepos, func(ctx context.Context, root string) error {
			_, refreshErr := refresh(ctx, root)
			return refreshErr
		})
	}
	if err != nil {
		return Response{}, err
	}
	defer store.Close()
	switch invocation.Command {
	case "symbols":
		response.Symbols, response.Truncated, err = store.FindSymbols(ctx, invocation.Arguments[0], invocation.Limit)
	case "context":
		var result contextquery.Result
		result, err = contextquery.Build(ctx, store, invocation.Arguments[0], contextOptions(invocation), federatedLocator(store))
		if err == nil {
			result.Metadata.Freshness.Checked = true
			result.Metadata.Freshness.Current = true
			response.Context = &result
			response.Truncated = contextTruncated(result.Metadata.Truncation)
		}
	case "explore":
		response.Contexts, response.Truncated, err = contextquery.Explore(ctx, store, invocation.Arguments[0], exploreOptions(invocation), federatedLocator(store))
		if err == nil {
			for index := range response.Contexts {
				response.Contexts[index].Metadata.Freshness.Checked = true
				response.Contexts[index].Metadata.Freshness.Current = true
				response.Contexts[index].Metadata.Freshness.Partial = store.Partial()
				response.Truncated = response.Truncated || contextTruncated(response.Contexts[index].Metadata.Truncation)
			}
		}
	case "graph":
		err = executeGraph(ctx, store, &response, invocation)
	case "workspace find", "workspace outline", "workspace links", "workspace backlinks":
		err = executeWorkspace(ctx, store, &response, invocation)
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
	case "dependencies":
		var symbol graph.Symbol
		if symbol, err = query.Resolve(ctx, store, invocation.Arguments[0]); err == nil {
			response.Edges, response.Truncated, err = directDependencies(ctx, store, symbol.ID, invocation.Limit)
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
			kinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeDependsOn, graph.EdgeImplements, graph.EdgeImports, graph.EdgeTests, graph.EdgeLinksTo, graph.EdgeEmbeds}
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
	if !strings.HasPrefix(invocation.Command, "workspace ") {
		if err := enrichResponse(ctx, store, &response); err != nil {
			return Response{}, fmt.Errorf("%s: enrich result: %w", invocation.Command, err)
		}
	}
	response.Diagnostics = append(freshnessDiagnostics, store.Diagnostics()...)
	slices.Sort(response.Diagnostics)
	response.Diagnostics = slices.Compact(response.Diagnostics)
	if response.Context != nil {
		response.Context.Metadata.Freshness.Partial = store.Partial()
	}
	response.Sources = store.Sources()
	return response, nil
}

func aggregateEligible(command string) bool {
	switch command {
	case "symbols", "callers", "callees", "dependencies", "path", "impact", "graph":
		return true
	default:
		return false
	}
}

type definitionStore interface {
	FindSymbols(context.Context, string, int) ([]graph.Symbol, bool, error)
	Occurrences(context.Context, string, []string, int) ([]graph.Occurrence, bool, error)
}

type displayStore interface {
	query.Store
	Document(context.Context, string) (graph.Document, bool, error)
}

func directDependencies(ctx context.Context, store query.Store, symbolID string, limit int) ([]graph.Edge, bool, error) {
	edges, storageTruncated, err := store.EdgesFrom(ctx, symbolID, []graph.EdgeKind{graph.EdgeDependsOn, graph.EdgeImports}, 100_000)
	if err != nil {
		return nil, false, err
	}
	byEndpoint := make(map[string]graph.Edge, len(edges))
	for _, edge := range edges {
		key := edge.From + "\x00" + edge.To
		current, exists := byEndpoint[key]
		if !exists || (current.Kind == graph.EdgeImports && edge.Kind == graph.EdgeDependsOn) {
			byEndpoint[key] = edge
		}
	}
	result := make([]graph.Edge, 0, len(byEndpoint))
	for _, edge := range byEndpoint {
		result = append(result, edge)
	}
	slices.SortFunc(result, func(a, b graph.Edge) int {
		if a.To != b.To {
			return strings.Compare(a.To, b.To)
		}
		return strings.Compare(a.ID, b.ID)
	})
	truncated := storageTruncated || len(result) > limit
	if len(result) > limit {
		result = result[:limit]
	}
	return result, truncated, nil
}

func enrichResponse(ctx context.Context, store displayStore, response *Response) error {
	symbols := make(map[string]graph.Symbol, len(response.Symbols))
	symbolIDs := make(map[string]bool, len(response.Nodes)+2*len(response.Edges)+len(response.Occurrences))
	documents := make(map[string]graph.Document, len(response.Documents))
	documentIDs := make(map[string]bool, len(response.Occurrences)+len(response.Edges)+len(response.Symbols))
	for _, symbol := range response.Symbols {
		symbols[symbol.ID] = symbol
		if symbol.DocumentID != "" {
			documentIDs[symbol.DocumentID] = true
		}
	}
	for _, document := range response.Documents {
		documents[document.ID] = document
	}
	for _, id := range response.Nodes {
		symbolIDs[id] = true
	}
	for _, edge := range response.Edges {
		symbolIDs[edge.From], symbolIDs[edge.To] = true, true
		if edge.DocumentID != "" {
			documentIDs[edge.DocumentID] = true
		}
	}
	for _, occurrence := range response.Occurrences {
		symbolIDs[occurrence.SymbolID] = true
		if occurrence.DocumentID != "" {
			documentIDs[occurrence.DocumentID] = true
		}
	}
	ids := make([]string, 0, len(symbolIDs))
	for id := range symbolIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if _, exists := symbols[id]; exists {
			continue
		}
		symbol, ok, err := store.Symbol(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		symbols[id] = symbol
		if symbol.DocumentID != "" {
			documentIDs[symbol.DocumentID] = true
		}
	}
	ids = ids[:0]
	for id := range documentIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if _, exists := documents[id]; exists {
			continue
		}
		document, ok, err := store.Document(ctx, id)
		if err != nil {
			return err
		}
		if ok {
			documents[id] = document
		}
	}
	response.Symbols = response.Symbols[:0]
	for _, symbol := range symbols {
		response.Symbols = append(response.Symbols, symbol)
	}
	slices.SortFunc(response.Symbols, func(a, b graph.Symbol) int {
		if a.StableName != b.StableName {
			return strings.Compare(a.StableName, b.StableName)
		}
		return strings.Compare(a.ID, b.ID)
	})
	response.Documents = response.Documents[:0]
	for _, document := range documents {
		response.Documents = append(response.Documents, document)
	}
	slices.SortFunc(response.Documents, func(a, b graph.Document) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return nil
}

func executeGraph(ctx context.Context, store query.Store, response *Response, invocation Invocation) error {
	focus, err := query.Resolve(ctx, store, invocation.Arguments[0])
	if err != nil {
		return err
	}
	direction := invocation.Direction
	if direction == "" {
		direction = query.DirectionBoth
	}
	kinds := invocation.Kinds
	if len(kinds) == 0 {
		kinds = defaultGraphKinds()
	}
	traversal, err := query.Neighborhood(ctx, store, focus.ID, kinds, direction, bounds(invocation))
	if err != nil {
		return err
	}
	response.Nodes, response.Edges, response.Truncated = traversal.Nodes, traversal.Edges, traversal.Truncated
	for _, id := range traversal.Nodes {
		symbol, ok, err := store.Symbol(ctx, id)
		if err != nil {
			return err
		}
		if ok {
			response.Symbols = append(response.Symbols, symbol)
		}
	}
	slices.SortFunc(response.Symbols, func(a, b graph.Symbol) int {
		if a.StableName != b.StableName {
			return strings.Compare(a.StableName, b.StableName)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return nil
}

func defaultGraphKinds() []graph.EdgeKind {
	return []graph.EdgeKind{
		graph.EdgeCalls, graph.EdgeImports, graph.EdgeContains, graph.EdgeExtends,
		graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeDependsOn, graph.EdgeTests,
		graph.EdgeGenerates, graph.EdgeDocuments, graph.EdgeExposes, graph.EdgeHandles,
		graph.EdgeReads, graph.EdgeWrites, graph.EdgeLinksTo, graph.EdgeEmbeds,
		graph.EdgeMemberOf, graph.EdgeResolvesTo,
	}
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
	return inspectAdaptersWithError(ctx, doctor, runner, nil)
}

func inspectAdaptersWithError(ctx context.Context, doctor bool, runner adapter.Runner, configErr error, registrations ...adapter.Registration) []AdapterStatus {
	return inspectAdaptersWithErrorAt(ctx, doctor, runner, configErr, ".", registrations...)
}

func inspectAdaptersWithErrorAt(ctx context.Context, doctor bool, runner adapter.Runner, configErr error, directory string, registrations ...adapter.Registration) []AdapterStatus {
	type candidate struct {
		name, kind, configured string
		command                []string
		expectedProvider       string
		registration           *adapter.Registration
		discover               bool
	}
	candidates := []candidate{
		{name: "weave-dotnet", kind: "native", configured: os.Getenv("WEAVE_DOTNET_ADAPTER"), expectedProvider: "weave-dotnet"},
		{name: "weave-python", kind: "native", configured: os.Getenv("WEAVE_PYTHON_ADAPTER"), expectedProvider: "weave-python"},
		{name: "weave-rust", kind: "native", configured: os.Getenv("WEAVE_RUST_ADAPTER"), expectedProvider: "weave-rust"},
		{name: "weave-cpp", kind: "native", configured: os.Getenv("WEAVE_CPP_ADAPTER"), expectedProvider: "scip:scip-clang"},
		{name: "weave-typescript", kind: "native", configured: os.Getenv("WEAVE_TYPESCRIPT_ADAPTER"), expectedProvider: "scip:scip-typescript"},
		{name: "weave-jvm", kind: "native", configured: os.Getenv("WEAVE_JVM_ADAPTER"), expectedProvider: "scip:scip-java"},
		{name: "scip-dotnet", kind: "scip-producer", configured: os.Getenv("WEAVE_SCIP_DOTNET")},
	}
	configured := append([]adapter.Registration(nil), registrations...)
	slices.SortFunc(configured, func(a, b adapter.Registration) int { return strings.Compare(a.Name, b.Name) })
	activations := map[string]string{}
	if doctor && len(configured) != 0 {
		activations = inspectAdapterActivations(ctx, directory, configured)
	}
	for _, registration := range configured {
		registration := registration
		candidates = slices.DeleteFunc(candidates, func(value candidate) bool {
			return value.name == registration.Name || value.expectedProvider == registration.Name
		})
		candidates = append(candidates, candidate{
			name: registration.Name, kind: "native", command: append([]string(nil), registration.Command...), expectedProvider: registration.Name, registration: &registration,
		})
	}
	if doctor {
		candidates = append(candidates, candidate{name: "dotnet", kind: "runtime", discover: true})
	}
	statuses := make([]AdapterStatus, 0, len(candidates))
	for _, candidate := range candidates {
		value := ""
		detail := "not configured or managed"
		var args []string
		if len(candidate.command) != 0 {
			value, args = candidate.command[0], candidate.command[1:]
			detail = "configured by adapter registry"
			if candidate.registration != nil {
				switch candidate.registration.Source {
				case "managed":
					detail = "managed adapter"
				case "environment":
					detail = "configured by environment"
				}
			}
		} else if candidate.configured != "" {
			value = candidate.configured
			detail = "configured by environment"
		} else if candidate.discover {
			value = candidate.name
			detail = "not found on PATH"
		}
		var path string
		var err error
		if value != "" {
			path, err = resolveExecutable(value)
		} else {
			err = errors.New("not configured")
		}
		status := AdapterStatus{Name: candidate.name, Kind: candidate.kind, Detail: detail}
		if candidate.registration != nil {
			status.Source = candidate.registration.Source
			status.Permissions = candidate.registration.Permissions
			claims := candidate.registration.Claims
			status.Claims = &claims
			if candidate.registration.IntegrityError != "" {
				status.Integrity = "unverified"
				if candidate.registration.ArtifactDigest != "" {
					status.Integrity = "failed"
				}
				status.Detail = candidate.registration.IntegrityError
			}
			status.CapabilityDigest = candidate.registration.CapabilityDigest
			status.Activation = activations[candidate.registration.Name]
			if candidate.registration.ArtifactDigest != "" && status.Integrity == "" {
				status.Integrity = "pinned"
			}
			if candidate.registration.PinnedCapabilities != nil {
				requires := candidate.registration.PinnedCapabilities.Requires
				status.Requires = &requires
				status.RequirementStatus = adapterRequirementStatuses(requires)
			}
		}
		if err == nil {
			status.Available, status.Path = true, path
			if candidate.configured == "" && len(candidate.command) == 0 {
				status.Detail = "discovered on PATH"
			}
		} else if len(candidate.command) != 0 {
			status.Detail = "registered command unavailable: " + err.Error()
		} else if candidate.configured != "" {
			status.Detail = "configured path unavailable: " + err.Error()
		}
		managedIntegrityFailure := candidate.registration != nil && candidate.registration.ArtifactDigest != "" && candidate.registration.IntegrityError != ""
		if doctor && candidate.registration != nil && candidate.registration.ArtifactDigest != "" {
			if !status.Available {
				managedIntegrityFailure = true
				status.Integrity = "failed"
			} else if verifyErr := adapter.VerifyArtifact(path, candidate.registration.ArtifactDigest); verifyErr != nil {
				managedIntegrityFailure = true
				status.Integrity = "failed"
				status.Detail = "artifact integrity failed: " + verifyErr.Error()
			} else {
				status.Integrity = "verified"
			}
		}
		if doctor && status.Available && candidate.kind == "native" && !managedIntegrityFailure {
			probeCtx, cancel := context.WithTimeout(ctx, adapterDoctorTimeout)
			capabilities, stderr, probeErr := runner.Describe(probeCtx, adapter.Executable{Path: path, Args: append([]string(nil), args...), Env: adapterEnvironment()})
			cancel()
			status.Checked = true
			if probeErr != nil {
				status.Detail = "protocol check failed: " + probeErr.Error()
				if stderr != "" {
					status.Detail += ": " + stderr
				}
			} else if candidate.expectedProvider != "" && capabilities.Provider.Name != candidate.expectedProvider {
				status.Detail = fmt.Sprintf("protocol check failed: adapter provider is %q, want %q", capabilities.Provider.Name, candidate.expectedProvider)
			} else {
				digest, _ := adapter.CapabilityDigest(capabilities)
				status.CapabilityDigest = digest
				claimDrift := ""
				if candidate.registration != nil && registrationHasClaims(*candidate.registration) {
					wantClaims, wantErr := adapter.ClaimsDigest(candidate.registration.Claims)
					gotClaims, gotErr := adapter.ClaimsDigest(capabilities.Claims)
					if wantErr != nil || gotErr != nil {
						claimDrift = errors.Join(wantErr, gotErr).Error()
					} else if wantClaims != gotClaims {
						claimDrift = fmt.Sprintf("got %s, configured %s", gotClaims, wantClaims)
					}
				}
				if claimDrift != "" {
					status.Detail = "claim drift: " + claimDrift
				} else if candidate.registration != nil && candidate.registration.CapabilityDigest != "" && digest != candidate.registration.CapabilityDigest {
					status.Detail = fmt.Sprintf("capability/claim drift: got %s, installed %s", digest, candidate.registration.CapabilityDigest)
				} else if candidate.registration != nil && candidate.registration.Source == "explicit" && candidate.registration.CapabilityDigest == "" {
					status.Detail = "capability pin required for automatic execution; set capability_digest to " + digest
				} else {
					status.Compatible = true
				}
				status.Provider, status.Version = capabilities.Provider.Name, capabilities.Provider.Version
				status.Languages = append([]string(nil), capabilities.Languages...)
				requires := capabilities.Requires
				status.Requires = &requires
				status.RequirementStatus = adapterRequirementStatuses(requires)
				slices.Sort(status.Languages)
				if status.Compatible {
					status.Detail = "compatible with " + adapter.Protocol
				}
			}
		}
		statuses = append(statuses, status)
	}
	if configErr != nil {
		statuses = append(statuses, AdapterStatus{Name: "registry", Kind: "configuration", Detail: configErr.Error()})
	}
	return statuses
}

func registrationHasClaims(value adapter.Registration) bool {
	return len(value.Claims.Inputs.Extensions)+len(value.Claims.Inputs.Filenames)+len(value.Claims.Inputs.ProjectMarkers)+len(value.Claims.Evidence) != 0 ||
		value.Claims.Fallback || value.Claims.InvalidationAllFiles
}

func inspectAdapterActivations(ctx context.Context, directory string, registrations []adapter.Registration) map[string]string {
	result := make(map[string]string, len(registrations))
	setAll := func(value string) map[string]string {
		for _, registration := range registrations {
			result[registration.Name] = value
		}
		return result
	}
	repo, err := repository.Discover(ctx, directory)
	if err != nil {
		return setAll("unknown: " + err.Error())
	}
	paths, err := repo.VisiblePaths(ctx)
	if err != nil {
		return setAll("unknown: " + err.Error())
	}
	routing := append(adapter.BuiltinRoutingClaims(), registrations...)
	routes, err := adapter.RouteInputs(paths, routing)
	if err != nil {
		return setAll("conflict: " + err.Error())
	}
	for _, registration := range registrations {
		count := len(routes[registration.Name])
		if count == 0 {
			result[registration.Name] = "inactive: no routed inputs in the current worktree"
			continue
		}
		result[registration.Name] = fmt.Sprintf("active: %d routed input(s) in the current worktree", count)
	}
	return result
}

func adapterRequirementStatuses(requires adapter.Requirements) []AdapterRequirementStatus {
	result := make([]AdapterRequirementStatus, 0, len(requires.Executables))
	for _, required := range requires.Executables {
		requirement := AdapterRequirementStatus{Name: required}
		if strings.ContainsAny(required, " <>=") {
			requirement.Detail = "declared requirement; version/range is adapter-owned"
		} else if path, findErr := exec.LookPath(required); findErr == nil {
			requirement.Available, requirement.Detail = true, path
		} else {
			requirement.Detail = findErr.Error()
		}
		result = append(result, requirement)
	}
	return result
}

func (app Local) manageAdapter(ctx context.Context, response Response, invocation Invocation) (Response, error) {
	store := app.AdapterStore
	if store.Root == "" {
		return Response{}, errors.New("adapter store is unavailable")
	}
	store.Runner = app.AdapterRunner
	switch invocation.Command {
	case "adapters install", "adapters update":
		item, err := store.Install(ctx, adapter.InstallOptions{
			Source: invocation.AdapterSource, UpdateName: invocation.AdapterName,
			Arguments: invocation.AdapterArgs, ArgumentsSet: invocation.AdapterArgsSet,
			Permissions: invocation.Permissions, PermissionsSet: invocation.AdapterPolicySet,
			Timeout: invocation.Timeout, TimeoutSet: invocation.AdapterTimeoutSet,
		})
		if err != nil {
			return Response{}, err
		}
		claims, requires := item.Capabilities.Claims, item.Capabilities.Requires
		action := "installed"
		if invocation.Command == "adapters update" {
			action = "updated"
		}
		response.Adapters = []AdapterStatus{{Name: item.Name, Kind: "native", Available: true, Checked: true, Compatible: true, Path: filepath.Join(store.Root, item.Artifact), Provider: item.Capabilities.Provider.Name, Version: item.Capabilities.Provider.Version, Languages: item.Capabilities.Languages, Detail: "managed adapter " + action, Source: "managed", Integrity: "verified", Claims: &claims, Requires: &requires}}
		return response, nil
	case "adapters remove":
		return response, store.Remove(ctx, invocation.AdapterName)
	default:
		return Response{}, fmt.Errorf("unsupported adapter command %q", invocation.Command)
	}
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
	allowed := []string{
		"PATH", "HOME", "USERPROFILE", "JAVA_HOME", "CARGO_HOME", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN",
		"WEAVE_RUST_ANALYZER", "WEAVE_SCIP_CLANG", "WEAVE_SCIP_TYPESCRIPT", "WEAVE_SCIP_JAVA", "WEAVE_SCIP_JAVA_VERSION", "WEAVE_SCIP_JAVA_METADATA_VERSION",
		"TMPDIR", "TMP", "TEMP", "SystemRoot", "WINDIR",
	}
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
	case "symbols", "context", "explore", "definition", "references", "callers", "callees", "dependencies", "path", "impact", "graph", "export", "verify", "workspace find", "workspace outline", "workspace links", "workspace backlinks":
		return true
	default:
		return false
	}
}

func bounds(invocation Invocation) query.Bounds {
	maxEdges := invocation.MaxEdges
	if maxEdges == 0 {
		maxEdges = invocation.Limit * 100
		if maxEdges < 1000 {
			maxEdges = 1000
		}
	}
	return query.Bounds{MaxDepth: invocation.MaxDepth, MaxNodes: invocation.Limit, MaxEdges: maxEdges}
}
