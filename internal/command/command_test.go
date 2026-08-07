package command_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/goindex"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
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
		{name: "dependencies", args: []string{"dependencies"}, want: "dependencies"},
		{name: "architecture check", args: []string{"architecture", "check"}, want: "architecture check"},
		{name: "repos add", args: []string{"repos", "add"}, want: "repos add"},
		{name: "repos remove", args: []string{"repos", "remove"}, want: "repos remove"},
		{name: "repos list", args: []string{"repos", "list"}, want: "repos list"},
		{name: "adapters list", args: []string{"adapters", "list"}, want: "adapters list"},
		{name: "adapters doctor", args: []string{"adapters", "doctor"}, want: "adapters doctor"},
		{name: "version", args: []string{"version"}, want: "version"},
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

type recordingApplication struct {
	invocations []application.Invocation
	err         error
}

func (a *recordingApplication) Execute(_ context.Context, invocation application.Invocation) (application.Response, error) {
	a.invocations = append(a.invocations, invocation)
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
		{name: "definition", args: []string{"definition", "authorize"}, contains: []string{"fixture:authorize\tfunction\tauthorize"}},
		{name: "references", args: []string{"references", "authorize"}, contains: []string{"fixture:authorize\treference\tfixture:main.go:8:3"}},
		{name: "callers", args: []string{"callers", "authorize"}, contains: []string{"fixture:handle\tcalls\tfixture:authorize"}},
		{name: "callees", args: []string{"callees", "HandleRequest"}, contains: []string{"fixture:handle\tcalls\tfixture:authorize"}},
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

type commandProvider struct{ calls int }

func (*commandProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "command-fixture", Version: "1"}
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
		Occurrences: []graph.Occurrence{{ID: "occ", UnitID: "fixture", SymbolID: "fixture:authorize", DocumentID: "fixture:main.go", Role: "reference", Range: rng, Provider: "fixture", Evidence: graph.EvidenceExact}},
		Edges:       []graph.Edge{{ID: "edge", UnitID: "fixture", From: "fixture:handle", To: "fixture:authorize", Kind: graph.EdgeCalls, Provider: "fixture", Evidence: graph.EvidenceExact}},
	}
}
