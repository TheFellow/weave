package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/goindex"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
	"github.com/TheFellow/weave/internal/storage"
	"github.com/scip-code/scip/bindings/go/scip"
	cli "github.com/urfave/cli/v3"
	"google.golang.org/protobuf/proto"
)

func TestPlaceholderCommandsSucceedSilently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "init", args: []string{"init"}, want: "init"},
		{name: "index", args: []string{"index"}, want: "index"},
		{name: "status", args: []string{"status"}, want: "status"},
		{name: "architecture check", args: []string{"architecture", "check"}, want: "architecture check"},
		{name: "links list", args: []string{"links", "list"}, want: "links list"},
		{name: "repos add", args: []string{"repos", "add"}, want: "repos add"},
		{name: "repos list", args: []string{"repos", "list"}, want: "repos list"},
		{name: "adapters list", args: []string{"adapters", "list"}, want: "adapters list"},
		{name: "adapters doctor", args: []string{"adapters", "doctor"}, want: "adapters doctor"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app := &recordingApplication{}
			root := command.New(app, command.Streams{
				Stdin:  strings.NewReader(""),
				Stdout: &stdout,
				Stderr: &stderr,
			})

			args := append([]string{"weave"}, test.args...)
			if err := root.Run(context.Background(), args); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := stdout.String(); got != "" {
				t.Errorf("stdout = %q, want empty", got)
			}
			if got := stderr.String(); got != "" {
				t.Errorf("stderr = %q, want empty", got)
			}

			want := []application.Invocation{{Command: test.want}}
			if !reflect.DeepEqual(app.invocations, want) {
				t.Errorf("invocations = %#v, want %#v", app.invocations, want)
			}
		})
	}
}

func TestApplicationErrorIsReturned(t *testing.T) {
	t.Parallel()

	want := errors.New("application failed")
	app := &recordingApplication{err: want}
	root := command.New(app, command.Streams{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})

	err := root.Run(context.Background(), []string{"weave", "index"})
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want %v", err, want)
	}
}

func TestWorkspaceCommandTreeForwardsBoundedFederatedQueries(t *testing.T) {
	t.Parallel()
	app := &recordingApplication{}
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err := root.Run(context.Background(), []string{"weave", "ws", "backlinks", "README.md#design", "--scope", "catalog", "--repo", "github.com/TheFellow/example", "--limit", "25", "--max-depth", "4"}); err != nil {
		t.Fatal(err)
	}
	want := application.Invocation{
		Command: "workspace backlinks", Arguments: []string{"README.md#design"}, Limit: 25, MaxDepth: 4,
		Scope: "catalog", Repositories: []string{"github.com/TheFellow/example"}, MaxRepos: 32,
	}
	if !reflect.DeepEqual(app.invocations, []application.Invocation{want}) {
		t.Fatalf("invocations = %#v, want %#v", app.invocations, want)
	}
}

func TestWorkspaceTextRenderingUsesStableNamesNotOpaqueIDs(t *testing.T) {
	var output, diagnostics bytes.Buffer
	response := application.Response{
		Command:   "workspace backlinks",
		Truncated: true,
		Symbols:   []graph.Symbol{{ID: "opaque-source-id", StableName: "README.md#design", DisplayName: "Design", Kind: "section", Evidence: graph.EvidenceSyntactic}},
		Edges:     []graph.Edge{{From: "opaque-source-id", To: "opaque-target-id", Kind: graph.EdgeLinksTo, Evidence: graph.EvidenceDeclared}},
		Freshness: &freshness.Status{Diagnostics: []string{"weave-workspace: degraded README.md"}},
	}
	app := &recordingApplication{response: response}
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &output, Stderr: &diagnostics})
	if err := root.Run(context.Background(), []string{"weave", "workspace", "backlinks", "README.md"}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "links-to\tREADME.md#design\tdeclared\n... truncated\n"; got != want {
		t.Fatalf("workspace output = %q, want %q", got, want)
	}
	if got, want := diagnostics.String(), "weave-workspace: degraded README.md\n"; got != want {
		t.Fatalf("workspace diagnostics = %q, want %q", got, want)
	}
}

func TestGraphCommandForwardsBoundedCatalogQuery(t *testing.T) {
	t.Parallel()
	app := &recordingApplication{response: application.Response{Command: "graph", Nodes: []string{"focus"}}}
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	err := root.Run(context.Background(), []string{
		"weave", "graph", "Handler", "--direction", "incoming", "--kind", "calls", "--kind", "implements",
		"--max-depth", "4", "--limit", "25", "--max-edges", "80", "--scope", "catalog",
		"--repo", "github.com/acme/service", "--max-repos", "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := application.Invocation{
		Command: "graph", Arguments: []string{"Handler"}, Limit: 25, MaxDepth: 4, MaxEdges: 80,
		Kinds: []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements}, Direction: query.DirectionIncoming,
		Scope: "catalog", Repositories: []string{"github.com/acme/service"}, MaxRepos: 4,
	}
	if !reflect.DeepEqual(app.invocations, []application.Invocation{want}) {
		t.Fatalf("invocations = %#v, want %#v", app.invocations, want)
	}
}

func TestRepositoryCatalogCommands(t *testing.T) {
	ctx := context.Background()
	repositoryRoot := commandRepository(t)
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	app := application.Local{}
	var stdout, stderr bytes.Buffer
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "repos", "add", repositoryRoot, "--catalog", catalogPath}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("add stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	root = command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "repos", "list", "--catalog", catalogPath}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "\tmain\tcurrent\t") || !strings.Contains(stdout.String(), repositoryRoot) {
		t.Fatalf("list stdout=%q", stdout.String())
	}
}

func TestArchitectureCheckTextJSONAndSARIF(t *testing.T) {
	ctx := context.Background()
	repositoryRoot := commandRepository(t)
	databasePath := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, databasePath, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(ctx, commandFixture()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "architecture.json")
	config := `{"schema":"weave.architecture/v1","layers":[{"id":"handler","symbols":["fixture.HandleRequest"]},{"id":"authorization","symbols":["fixture.authorize"]}],"rules":[{"id":"handler-no-auth","action":"forbid","from":"handler","to":"authorization","kinds":["calls"],"message":"route through the policy boundary"}]}`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	app := application.Local{DatabasePath: databasePath, Directory: repositoryRoot}
	for _, test := range []struct {
		name     string
		format   string
		contains string
	}{
		{"text", "text", "handler-no-auth\tcalls"},
		{"json", "json", `"schema":"weave.architecture-result/v1"`},
		{"sarif", "sarif", `"version":"2.1.0"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
			err := root.Run(ctx, []string{"weave", "arch", "check", "--config", configPath, "--format", test.format})
			var exit cli.ExitCoder
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("Run error = %v", err)
			}
			if !strings.Contains(stdout.String(), test.contains) {
				t.Fatalf("stdout = %q", stdout.String())
			}
			if test.format == "sarif" {
				var value map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
					t.Fatalf("invalid SARIF JSON: %v", err)
				}
			}
		})
	}
}

func TestCIKeyIndexAndCheckWorkflow(t *testing.T) {
	ctx := context.Background()
	repositoryRoot := commandRepository(t)
	provider := &unresolvedCommandProvider{}
	manager := &freshness.Manager{Directory: repositoryRoot, Provider: provider, Command: "test"}
	app := application.Local{Freshness: manager}

	var stdout, stderr bytes.Buffer
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "ci", "key"}); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(key, "weave-v1-") || stderr.Len() != 0 {
		t.Fatalf("key stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	root = command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "ci", "index"}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 || provider.calls != 1 {
		t.Fatalf("index stdout=%q stderr=%q calls=%d", stdout.String(), stderr.String(), provider.calls)
	}

	root = command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "ci", "check"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "warning\tunresolved-occurrence") {
		t.Fatalf("text check hid integrity diagnostic: %q", stdout.String())
	}
	stdout.Reset()
	root = command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "ci", "check", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Issues       []storage.Issue `json:"issues"`
		Architecture json.RawMessage `json:"architecture"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || len(report.Issues) != 1 || len(report.Architecture) == 0 {
		t.Fatalf("JSON check hid results: %q, %#v, %v", stdout.String(), report, err)
	}
	stdout.Reset()
	root = command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(ctx, []string{"weave", "ci", "check", "--format", "sarif"}); err != nil {
		t.Fatal(err)
	}
	var sarif map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &sarif); err != nil || sarif["version"] != "2.1.0" {
		t.Fatalf("SARIF = %q, %v", stdout.String(), err)
	}
	if !strings.Contains(stdout.String(), "weave/integrity/unresolved-occurrence") || !strings.Contains(stdout.String(), `"level":"warning"`) {
		t.Fatalf("SARIF hid integrity diagnostic: %q", stdout.String())
	}
	if provider.calls != 1 {
		t.Fatalf("current check unexpectedly reindexed: calls=%d", provider.calls)
	}
}

func TestInvalidInvocationsReturnErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"unknown"}},
		{name: "unknown nested command", args: []string{"repos", "unknown"}},
		{name: "unknown flag", args: []string{"status", "--unknown"}},
		{name: "unexpected argument", args: []string{"status", "unexpected"}},
		{name: "missing symbol", args: []string{"symbols"}},
		{name: "extra path argument", args: []string{"path", "a", "b", "c"}},
		{name: "invalid limit", args: []string{"symbols", "x", "--limit", "0"}},
		{name: "invalid edge kind", args: []string{"impact", "x", "--kind", "magic"}},
		{name: "invalid max depth", args: []string{"path", "x", "y", "--max-depth", "0"}},
		{name: "conflicting external index", args: []string{"index", "--scip", "index.scip", "--adapter", "tool"}},
		{name: "orphan adapter argument", args: []string{"index", "--adapter-arg", "--flag"}},
		{name: "invalid adapter timeout", args: []string{"index", "--adapter", "tool", "--timeout", "0s"}},
		{name: "missing repository selector", args: []string{"repos", "remove"}},
		{name: "missing graph target", args: []string{"graph"}},
		{name: "invalid graph direction", args: []string{"graph", "x", "--direction", "sideways"}},
		{name: "invalid graph edge bound", args: []string{"graph", "x", "--max-edges", "0"}},
		{name: "JSON graph file", args: []string{"graph", "x", "--json", "--output", "graph.dot"}},
		{name: "interactive JSON graph", args: []string{"graph", "x", "--interactive", "--json"}},
		{name: "interactive graph file", args: []string{"graph", "x", "--interactive", "--output", "graph.dot"}},
		{name: "orphan no-open", args: []string{"graph", "x", "--no-open"}},
		{name: "link add missing endpoints", args: []string{"links", "add", "docs-code", "--kind", "documents"}},
		{name: "link add invalid kind", args: []string{"links", "add", "docs-code", "--from", "README", "--to", "Serve", "--kind", "magic"}},
		{name: "link update empty patch", args: []string{"links", "update", "docs-code"}},
		{name: "link remove missing ID", args: []string{"links", "remove"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app := &recordingApplication{}
			root := command.New(app, command.Streams{
				Stdin:  strings.NewReader(""),
				Stdout: &stdout,
				Stderr: &stderr,
			})

			args := append([]string{"weave"}, test.args...)
			if err := root.Run(context.Background(), args); err == nil {
				t.Fatal("Run() error = nil, want usage error")
			}
			if len(app.invocations) != 0 {
				t.Errorf("application invoked on invalid input: %#v", app.invocations)
			}
		})
	}
}

func TestExplicitExternalIndexFlagsReachApplication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want application.Invocation
	}{
		{"scip", []string{"index", "--scip", "index.scip"}, application.Invocation{Command: "index", SCIPPath: "index.scip"}},
		{"adapter", []string{"index", "--adapter", "fixture-adapter", "--adapter-arg=--project=x", "--timeout", "3s", "--allow-build-tool"}, application.Invocation{
			Command: "index", AdapterPath: "fixture-adapter", AdapterArgs: []string{"--project=x"}, Timeout: 3 * time.Second,
			Permissions: adapter.Permissions{BuildTool: true},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := &recordingApplication{}
			root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
			if err := root.Run(context.Background(), append([]string{"weave"}, test.args...)); err != nil {
				t.Fatal(err)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 || !reflect.DeepEqual(app.invocations, []application.Invocation{test.want}) {
				t.Fatalf("stdout=%q stderr=%q invocations=%#v", stdout.String(), stderr.String(), app.invocations)
			}
		})
	}
}

func TestFederationFlagsReachApplication(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &recordingApplication{}
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(context.Background(), []string{"weave", "callers", "global", "--scope", "catalog", "--repo", "github.com/acme/a", "--catalog", "/tmp/weave-catalog.db", "--max-repos", "4"}); err != nil {
		t.Fatal(err)
	}
	if len(app.invocations) != 1 {
		t.Fatalf("invocations = %#v", app.invocations)
	}
	got := app.invocations[0]
	if got.Scope != "catalog" || !reflect.DeepEqual(got.Repositories, []string{"github.com/acme/a"}) || got.CatalogPath != "/tmp/weave-catalog.db" || got.MaxRepos != 4 {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestContextualLinkFlagsReachApplicationAndRenderExactDeclaration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &recordingApplication{response: application.Response{Command: "links add", Links: []bridge.Link{{
		ID: "guide-code", From: "entity:workspace:guide", To: "entity:scip:Serve", Kind: graph.EdgeDocuments, Note: "Keeps docs and code together.\nReviewed.",
	}}}}
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(context.Background(), []string{
		"weave", "links", "add", "guide-code", "--from", "docs/guide.md", "--to", "Serve",
		"--kind", "documents", "--note", "Keeps docs and code together.\nReviewed.",
		"--scope", "catalog", "--repo", "github.com/acme/docs", "--max-repos", "4",
	}); err != nil {
		t.Fatal(err)
	}
	if len(app.invocations) != 1 {
		t.Fatalf("invocations = %#v", app.invocations)
	}
	got := app.invocations[0]
	if got.Command != "links add" || got.LinkFrom != "docs/guide.md" || got.LinkTo != "Serve" || got.LinkKind != graph.EdgeDocuments || got.LinkNote == "" ||
		!got.LinkFromSet || !got.LinkToSet || !got.LinkKindSet || !got.LinkNoteSet || got.Scope != "catalog" || got.MaxRepos != 4 {
		t.Fatalf("invocation = %#v", got)
	}
	if want := "guide-code\tdocuments\tentity:workspace:guide\tentity:scip:Serve\t\"Keeps docs and code together.\\nReviewed.\"\n"; stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestContextualLinkUpdateCanClearNote(t *testing.T) {
	var stdout bytes.Buffer
	app := &recordingApplication{}
	root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err := root.Run(context.Background(), []string{"weave", "link", "update", "guide-code", "--note", ""}); err != nil {
		t.Fatal(err)
	}
	if len(app.invocations) != 1 || !app.invocations[0].LinkNoteSet || app.invocations[0].LinkNote != "" {
		t.Fatalf("invocations = %#v", app.invocations)
	}
}

func TestAdapterDoctorReportsMissingToolsWithoutExecutingThem(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("WEAVE_DOTNET_ADAPTER", "")
	t.Setenv("WEAVE_PYTHON_ADAPTER", "")
	t.Setenv("WEAVE_SCIP_DOTNET", "")
	var stdout, stderr bytes.Buffer
	root := command.New(application.Local{}, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := root.Run(context.Background(), []string{"weave", "adapters", "doctor"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"weave-dotnet\tnative\tmissing", "weave-python\tnative\tmissing", "scip-dotnet\tscip-producer\tmissing", "dotnet\truntime\tmissing"} {
		if !strings.Contains(stdout.String(), name) {
			t.Errorf("stdout = %q, want %q", stdout.String(), name)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAdaptersListReportsRegisteredAdapterWithoutExecutingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	program := filepath.Join(directory, "custom-adapter")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf executed > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	registration := adapter.Registration{
		Name: "custom-adapter", Command: []string{program, marker}, Inputs: adapter.Inputs{Extensions: []string{".custom"}},
	}
	var stdout, stderr bytes.Buffer
	root := command.New(application.Local{Adapters: []adapter.Registration{registration}}, command.Streams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if err := root.Run(context.Background(), []string{"weave", "adapters", "list"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("adapters list executed registered command: %v", err)
	}
	if !strings.Contains(stdout.String(), "custom-adapter\tnative\tavailable") || !strings.Contains(stdout.String(), "configured by adapter registry") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAdaptersListSurfacesRegistryConfigurationError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := command.New(application.Local{AdapterConfigError: errors.New("invalid registry fixture")}, command.Streams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if err := root.Run(context.Background(), []string{"weave", "adapters", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "registry\tconfiguration\tmissing") || !strings.Contains(stdout.String(), "invalid registry fixture") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

type recordingApplication struct {
	invocations []application.Invocation
	err         error
	response    application.Response
}

func (a *recordingApplication) Execute(_ context.Context, invocation application.Invocation) (application.Response, error) {
	a.invocations = append(a.invocations, invocation)
	if a.response.Command != "" {
		return a.response, a.err
	}
	return application.Response{Schema: application.QuerySchema, Command: invocation.Command}, a.err
}

func TestRealQueryCommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	facts := commandFixture()
	if err := db.ReplaceUnit(ctx, facts); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     []string
		contains []string
		empty    bool
	}{
		{name: "symbols", args: []string{"symbols", "handle"}, contains: []string{"fixture:handle\tfunction\tHandleRequest"}},
		{name: "definition", args: []string{"definition", "authorize"}, contains: []string{"fixture:authorize\tdefinition\tfixture:main.go:2:3", "fixture:authorize\tdefinition\tfixture:main.go:8:3"}},
		{name: "definition anchor fallback", args: []string{"definition", "HandleRequest"}, contains: []string{"fixture:handle\tfunction\tHandleRequest"}},
		{name: "references", args: []string{"references", "authorize"}, contains: []string{"fixture:authorize\treference\tfixture:main.go:8:3"}},
		{name: "callers", args: []string{"callers", "authorize"}, contains: []string{"fixture:handle\tcalls\tfixture:authorize"}},
		{name: "callees", args: []string{"callees", "HandleRequest"}, contains: []string{"fixture:handle\tcalls\tfixture:authorize"}},
		{name: "dependencies", args: []string{"dependencies", "HandleRequest"}, contains: []string{"fixture:handle\tdepends-on\tfixture:authorize"}},
		{name: "path", args: []string{"path", "HandleRequest", "authorize"}, contains: []string{"fixture:handle\tcalls\tfixture:authorize"}},
		{name: "impact", args: []string{"impact", "authorize"}, contains: []string{"fixture:handle\tcalls\tfixture:authorize"}},
		{name: "empty text", args: []string{"symbols", "missing"}, empty: true},
		{name: "empty json", args: []string{"symbols", "missing", "--json"}, contains: []string{`"schema":"weave.query/v1"`, `"query":["missing"]`, `"truncated":false`}},
		{name: "export", args: []string{"export", "--json"}, contains: []string{`"schema":"weave.export/v1"`, `"fixture:handle"`}},
		{name: "verify", args: []string{"verify"}, empty: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := command.New(application.Local{DatabasePath: path}, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
			if err := root.Run(ctx, append([]string{"weave"}, test.args...)); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if test.empty && stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			for _, value := range test.contains {
				if !strings.Contains(stdout.String(), value) {
					t.Errorf("stdout = %q, want substring %q", stdout.String(), value)
				}
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestGraphCommandWritesReadableDOTOrVersionedJSON(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, databasePath, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(ctx, commandFixture()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		root := command.New(application.Local{DatabasePath: databasePath}, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		err := root.Run(ctx, append([]string{"weave"}, args...))
		return stdout.String(), stderr.String(), err
	}
	stdout, stderr, err := run("graph", "HandleRequest", "--direction", "outgoing", "--kind", "calls", "--max-depth", "2", "--limit", "10", "--max-edges", "20")
	if err != nil || stderr != "" {
		t.Fatalf("graph stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	for _, want := range []string{"digraph weave {", "HandleRequest", `label="calls"`, `fillcolor="#F6C85F"`, `fillcolor="#D9D2E9"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("DOT output = %q, want %q", stdout, want)
		}
	}
	stdout, stderr, err = run("graph", "HandleRequest", "--direction", "outgoing", "--max-depth", "1", "--limit", "10", "--max-edges", "20")
	if err != nil || stderr != "" || strings.Contains(stdout, `label="references"`) || !strings.Contains(stdout, `label="calls"`) {
		t.Fatalf("default graph kinds stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}

	stdout, stderr, err = run("graph", "HandleRequest", "--kind", "calls", "--json")
	if err != nil || stderr != "" || !strings.Contains(stdout, `"schema":"weave.query/v1"`) || !strings.Contains(stdout, `"command":"graph"`) || !strings.Contains(stdout, `"nodes":["fixture:handle","fixture:authorize"]`) {
		t.Fatalf("JSON graph stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}

	outputPath := filepath.Join(t.TempDir(), "handler.dot")
	stdout, stderr, err = run("graph", "HandleRequest", "--kind", "calls", "--output", outputPath)
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("file graph stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil || !bytes.Contains(content, []byte("digraph weave {")) {
		t.Fatalf("DOT file = %q, %v", content, err)
	}
}

func TestVersionTextAndJSONRequireNoRepository(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "text", args: []string{"version"}, want: []string{"weave ", "go1."}},
		{name: "json", args: []string{"version", "--json"}, want: []string{`"schema":"weave.query/v1"`, `"command":"version"`, `"version":{"version":`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := command.New(application.Local{}, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
			if err := root.Run(context.Background(), append([]string{"weave"}, test.args...)); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("stdout = %q, want %q", stdout.String(), want)
				}
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestGCCommandCompactsClosedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(ctx, commandFixture()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	root := command.New(application.Local{DatabasePath: path}, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err := root.Run(ctx, []string{"weave", "gc"}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestLifecycleAndQueriesRefreshRepositoryBeforeReading(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := commandRepository(t)
	provider := &commandProvider{}
	manager := &freshness.Manager{Directory: root, Provider: provider, Command: "weave test"}
	app := application.Local{Freshness: manager}

	run := func(args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		if err := rootCommand.Run(ctx, append([]string{"weave"}, args...)); err != nil {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
		return stdout.String(), stderr.String()
	}

	stdout, stderr := run("status")
	if !strings.Contains(stdout, "current\tfalse") || !strings.Contains(stdout, "reason\tindex is not initialized") || stderr != "" {
		t.Fatalf("initial status stdout=%q stderr=%q", stdout, stderr)
	}
	stdout, stderr = run("init")
	if stdout != "" || !strings.Contains(stderr, "index: refreshed 0 changed paths") || provider.calls != 1 {
		t.Fatalf("init stdout=%q stderr=%q calls=%d", stdout, stderr, provider.calls)
	}
	stdout, stderr = run("symbols", "handle")
	if !strings.Contains(stdout, "fixture:handle") || stderr != "" || provider.calls != 1 {
		t.Fatalf("current query stdout=%q stderr=%q calls=%d", stdout, stderr, provider.calls)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr = run("symbols", "handle")
	if !strings.Contains(stdout, "fixture:handle") || !strings.Contains(stderr, "index: refreshed 1 changed paths") || provider.calls != 2 {
		t.Fatalf("dirty query stdout=%q stderr=%q calls=%d", stdout, stderr, provider.calls)
	}
}

func TestNativeGoProviderRefreshesBeforeEndToEndQuery(t *testing.T) {
	ctx := context.Background()
	root := commandRepository(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/commandfixture\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n\nfunc HandleRequest() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &freshness.Manager{Directory: root, Provider: goindex.Provider{}, Command: "weave test"}
	app := application.Local{Freshness: manager}

	run := func(query string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		if err := rootCommand.Run(ctx, []string{"weave", "symbols", query}); err != nil {
			t.Fatalf("symbols %q: %v", query, err)
		}
		return stdout.String(), stderr.String()
	}

	stdout, stderr := run("HandleRequest")
	if !strings.Contains(stdout, "HandleRequest") || !strings.Contains(stdout, "function") || !strings.Contains(stderr, "index: refreshed") {
		t.Fatalf("initial query stdout=%q stderr=%q", stdout, stderr)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n\nfunc HandleRequest() {}\nfunc AddedLater() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr = run("AddedLater")
	if !strings.Contains(stdout, "AddedLater") || !strings.Contains(stderr, "index: refreshed 2 changed paths") {
		t.Fatalf("query after edit stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestGitDiffImpactFindsAffectedGoTestThroughExecutableCLI(t *testing.T) {
	ctx := context.Background()
	root := commandRepository(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/impactfixture\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n\nfunc Handle() string { return \"ok\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package fixture\n\nimport \"testing\"\nfunc TestHandle(t *testing.T) { if Handle() == \"\" { t.Fatal(\"empty\") } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "impact baseline"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n\nfunc Handle() string { return \"changed\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &freshness.Manager{Directory: root, Provider: goindex.Provider{}, Command: "weave test"}
	app := application.Local{Freshness: manager}
	var stdout, stderr bytes.Buffer
	rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := rootCommand.Run(ctx, []string{"weave", "impact", "--git-diff", "HEAD", "--limit", "100"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "test\t") || !strings.Contains(stdout.String(), "TestHandle") {
		t.Fatalf("impact stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestSCIPImportIsQueryableAndMalformedReplacementIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := commandRepository(t)
	database := filepath.Join(t.TempDir(), "index.db")
	indexPath := filepath.Join(t.TempDir(), "fixture.scip")
	index := &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "fixture-indexer", Version: "1.0.0"}},
		Documents: []*scip.Document{{
			RelativePath: "main.cs", Language: "csharp", Text: "Name\n",
			PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
			Occurrences: []*scip.Occurrence{{
				TypedRange: &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{Line: 0, EndCharacter: 4}},
				Symbol:     "local 0", SymbolRoles: int32(scip.SymbolRole_Definition),
			}},
			Symbols: []*scip.SymbolInformation{{Symbol: "local 0", DisplayName: "Name", Kind: scip.SymbolInformation_Class}},
		}},
	}
	encoded, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	app := application.Local{DatabasePath: database, Directory: root}
	run := func(args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		err := rootCommand.Run(ctx, append([]string{"weave"}, args...))
		return stdout.String(), stderr.String(), err
	}
	stdout, stderr, err := run("index", "--scip", indexPath)
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("index stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = run("symbols", "Name")
	if err != nil || !strings.Contains(stdout, "\tclass\tName\t") || stderr != "" {
		t.Fatalf("query stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if err := os.WriteFile(indexPath, encoded[:len(encoded)-1], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = run("index", "--scip", indexPath); err == nil {
		t.Fatal("truncated replacement succeeded")
	}
	stdout, _, err = run("symbols", "Name")
	if err != nil || !strings.Contains(stdout, "\tclass\tName\t") {
		t.Fatalf("prior facts not preserved after failed replacement: stdout=%q err=%v", stdout, err)
	}
}

func TestSCIPProducerInventoriesCoexistAndReplaceSelectively(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := commandRepository(t)
	database := filepath.Join(t.TempDir(), "index.db")
	indexPath := filepath.Join(t.TempDir(), "fixture.scip")
	app := application.Local{DatabasePath: database, Directory: root}
	run := func(args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		err := rootCommand.Run(ctx, append([]string{"weave"}, args...))
		return stdout.String(), stderr.String(), err
	}
	importIndex := func(tool, version, symbol string) {
		t.Helper()
		encoded := scipProducerIndex(t, tool, version, symbol)
		if err := os.WriteFile(indexPath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, err := run("index", "--scip", indexPath)
		if err != nil || stdout != "" || stderr != "" {
			t.Fatalf("import %s stdout=%q stderr=%q err=%v", tool, stdout, stderr, err)
		}
	}
	query := func(symbol string) string {
		t.Helper()
		stdout, stderr, err := run("symbols", symbol)
		if err != nil || stderr != "" {
			t.Fatalf("query %s stdout=%q stderr=%q err=%v", symbol, stdout, stderr, err)
		}
		return stdout
	}

	// Both producers intentionally own the same path and local SCIP ID. Their
	// provider-qualified Weave identities must still coexist.
	importIndex("producer-alpha", "1.0.0", "Alpha")
	importIndex("producer-beta", "1.0.0", "Beta")
	if !strings.Contains(query("Alpha"), "Alpha") || !strings.Contains(query("Beta"), "Beta") {
		t.Fatal("producer facts did not coexist")
	}

	// An empty complete v2 inventory removes v1 alpha facts while preserving
	// beta. Version changes replace within the stable producer-name scope.
	importIndex("producer-alpha", "2.0.0", "")
	if query("Alpha") != "" || !strings.Contains(query("Beta"), "Beta") {
		t.Fatal("empty alpha inventory did not remove selectively")
	}

	importIndex("producer-alpha", "2.0.0", "AlphaAgain")
	valid := scipProducerIndex(t, "producer-alpha", "3.0.0", "Broken")
	if err := os.WriteFile(indexPath, valid[:len(valid)-1], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("index", "--scip", indexPath); err == nil {
		t.Fatal("truncated producer replacement succeeded")
	}
	if !strings.Contains(query("AlphaAgain"), "AlphaAgain") || !strings.Contains(query("Beta"), "Beta") {
		t.Fatal("failed producer replacement changed published inventories")
	}
}

func scipProducerIndex(t *testing.T, tool, version, symbol string) []byte {
	t.Helper()
	index := &scip.Index{Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: tool, Version: version}}}
	if symbol != "" {
		index.Documents = []*scip.Document{{
			RelativePath: "shared.cs", Language: "csharp", Text: symbol + "\n",
			PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
			Occurrences: []*scip.Occurrence{{
				TypedRange: &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{Line: 0, EndCharacter: int32(len(symbol))}},
				Symbol:     "local 0", SymbolRoles: int32(scip.SymbolRole_Definition),
			}},
			Symbols: []*scip.SymbolInformation{{Symbol: "local 0", DisplayName: symbol, Kind: scip.SymbolInformation_Class}},
		}}
	}
	encoded, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestExplicitAdapterIndexPublishesFactsAndDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := commandRepository(t)
	app := application.Local{DatabasePath: filepath.Join(t.TempDir(), "index.db"), Directory: root}
	var stdout, stderr bytes.Buffer
	rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	err := rootCommand.Run(ctx, []string{
		"weave", "index", "--adapter", os.Args[0],
		"--adapter-arg=-test.run=TestExternalAdapterHelperProcess", "--adapter-arg=--",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "warning: fixture warning") || !strings.Contains(stderr.String(), "adapter operator note") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	rootCommand = command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
	if err := rootCommand.Run(ctx, []string{"weave", "symbols", "AdapterSymbol"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "AdapterSymbol") {
		t.Fatalf("query stdout=%q", stdout.String())
	}
}

func TestExternalAdapterHelperProcess(t *testing.T) {
	separator := -1
	for i, argument := range os.Args {
		if argument == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	operation := os.Args[separator+1]
	provider := map[string]any{"name": "fixture-native", "version": "1.0.0"}
	if operation == "describe" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"protocols": []string{adapter.Protocol}, "provider": provider, "languages": []string{"fixture"},
			"operations": []string{"index"}, "refresh_modes": []string{"full"},
			"fact_encoding": adapter.FactEncoding, "position_encodings": []string{"utf8-byte"},
		})
		os.Exit(0)
	}
	if operation != "index" {
		os.Exit(90)
	}
	var request map[string]any
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(91)
	}
	requestID, _ := request["request_id"].(string)
	unit := graph.Unit{ID: "adapter:unit", Provider: "fixture-native", ProviderVersion: "1.0.0", Language: "fixture"}
	document := graph.Document{ID: "adapter:document", UnitID: unit.ID, Path: "adapter.fixture", Language: "fixture", Provider: unit.Provider, ProviderVersion: unit.ProviderVersion}
	symbol := graph.Symbol{ID: "adapter:symbol", UnitID: unit.ID, StableName: "fixture AdapterSymbol", DisplayName: "AdapterSymbol", Kind: "function", DocumentID: document.ID, Provider: unit.Provider, Evidence: graph.EvidenceExact}
	writeExternalFrame(requestID, "run.begin", map[string]any{"provider": provider, "fact_encoding": adapter.FactEncoding})
	writeExternalFrame(requestID, "unit.begin", map[string]any{"unit": unit})
	writeExternalFrame(requestID, "facts", map[string]any{"documents": []graph.Document{document}})
	writeExternalFrame(requestID, "facts", map[string]any{"symbols": []graph.Symbol{symbol}})
	writeExternalFrame(requestID, "diagnostic", map[string]any{"severity": "warning", "message": "fixture warning", "unit_id": unit.ID})
	writeExternalFrame(requestID, "unit.end", map[string]any{"status": "complete", "counts": map[string]int{"documents": 1, "symbols": 1, "occurrences": 0, "edges": 0}})
	writeExternalFrame(requestID, "run.end", map[string]any{"status": "complete", "units": []string{unit.ID}})
	_, _ = fmt.Fprintln(os.Stderr, "adapter operator note")
	os.Exit(0)
}

func writeExternalFrame(requestID, kind string, payload any) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"protocol": adapter.Protocol, "request_id": requestID, "kind": kind, "payload": payload,
	})
}

type commandProvider struct{ calls int }

func (*commandProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "command-fixture", Version: "1"}
}

type unresolvedCommandProvider struct{ calls int }

func (*unresolvedCommandProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "unresolved-command-fixture", Version: "1"}
}

func (p *unresolvedCommandProvider) Refresh(context.Context, freshness.Request) (freshness.Result, error) {
	p.calls++
	facts := commandFixture()
	facts.Occurrences = append(facts.Occurrences, graph.Occurrence{
		ID: "external-occurrence", UnitID: facts.Unit.ID,
		SymbolID: "scip go builtin builtin 1 error#", DocumentID: facts.Documents[0].ID,
		Role: "reference", Range: facts.Symbols[0].Definition,
		Provider: facts.Unit.Provider, Evidence: graph.EvidenceExact,
	})
	return freshness.Result{
		Batches: []graph.UnitFacts{facts},
		Units:   []freshness.Unit{{ID: facts.Unit.ID, InventoryDigest: "fixture-with-external-v1"}},
	}, nil
}

func (p *commandProvider) Refresh(context.Context, freshness.Request) (freshness.Result, error) {
	p.calls++
	return freshness.Result{
		Batches: []graph.UnitFacts{commandFixture()},
		Units:   []freshness.Unit{{ID: "fixture", InventoryDigest: "fixture-v1"}},
	}, nil
}

func commandRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	for _, invocation := range []struct {
		dir  string
		args []string
	}{
		{"", []string{"init", "--initial-branch=main", root}},
		{root, []string{"config", "user.email", "weave@example.test"}},
		{root, []string{"config", "user.name", "Weave Test"}},
	} {
		cmd := exec.Command("git", invocation.args...)
		cmd.Dir = invocation.dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", invocation.args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func commandFixture() graph.UnitFacts {
	rng := graph.Range{Start: graph.Position{Line: 7, Column: 2, Byte: 70}, End: graph.Position{Line: 7, Column: 11, Byte: 79}}
	return graph.UnitFacts{
		Unit:      graph.Unit{ID: "fixture", Provider: "fixture", ProviderVersion: "1"},
		Documents: []graph.Document{{ID: "fixture:main.go", UnitID: "fixture", Path: "main.go", Language: "go", Provider: "fixture", ProviderVersion: "1"}},
		Symbols: []graph.Symbol{
			{ID: "fixture:handle", UnitID: "fixture", StableName: "fixture.HandleRequest", DisplayName: "HandleRequest", Kind: "function", DocumentID: "fixture:main.go", Definition: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
			{ID: "fixture:authorize", UnitID: "fixture", StableName: "fixture.authorize", DisplayName: "authorize", Kind: "function", DocumentID: "fixture:main.go", Definition: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
		},
		Occurrences: []graph.Occurrence{
			{ID: "def-one", UnitID: "fixture", SymbolID: "fixture:authorize", DocumentID: "fixture:main.go", Role: "definition", Range: graph.Range{Start: graph.Position{Line: 1, Column: 2, Byte: 10}, End: graph.Position{Line: 1, Column: 11, Byte: 19}}, Provider: "fixture", Evidence: graph.EvidenceExact},
			{ID: "def-two", UnitID: "fixture", SymbolID: "fixture:authorize", DocumentID: "fixture:main.go", Role: "definition", Range: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
			{ID: "occ", UnitID: "fixture", SymbolID: "fixture:authorize", DocumentID: "fixture:main.go", Role: "reference", Range: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
		},
		Edges: []graph.Edge{
			{ID: "edge", UnitID: "fixture", From: "fixture:handle", To: "fixture:authorize", Kind: graph.EdgeCalls, Provider: "fixture", Evidence: graph.EvidenceExact},
			{ID: "dependency", UnitID: "fixture", From: "fixture:handle", To: "fixture:authorize", Kind: graph.EdgeDependsOn, Provider: "fixture", Evidence: graph.EvidenceDeclared},
		},
	}
}
