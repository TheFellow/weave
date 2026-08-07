package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/graph"
)

var fixturePaths = []string{
	".gitignore",
	"CMakeLists.txt",
	"deploy.sh",
	"main.lua",
	"queries.sql",
	"schema.proto",
}

type fakeTools struct {
	t         *testing.T
	version   string
	features  string
	formats   string
	languages string
	mappings  string
	kinds     string
	roles     string
	fields    string
	extras    string
	indexDir  string
	indexArgs []string
	calls     [][]string
}

func newFakeTools(t *testing.T) *fakeTools {
	return &fakeTools{
		t: t, version: defaultVersion, features: "json\nwildcards\n",
		formats: "u-ctags\njson\n", languages: "CMake\nLua\nProtobuf\nSQL\nSh\n",
		mappings: "Lua *.lua\nProtobuf *.proto\n", kinds: "Lua f function enabled\n",
		roles: "Lua function def enabled\n", fields: "N name enabled\n", extras: "r reference disabled\n",
	}
}

func (fake *fakeTools) lookPath(name string) (string, error) {
	switch name {
	case "fake-uctags":
		return filepath.Join(string(filepath.Separator), "fake", "uctags"), nil
	case "git":
		return filepath.Join(string(filepath.Separator), "fake", "git"), nil
	default:
		return "", exec.ErrNotFound
	}
}

func (fake *fakeTools) run(_ context.Context, path string, arguments []string, directory string, environment []string, limits outputLimits) (commandResult, error) {
	fake.t.Helper()
	if limits.stdout <= 0 || limits.stderr <= 0 {
		fake.t.Fatalf("unbounded command limits: %#v", limits)
	}
	copyArguments := slices.Clone(arguments)
	fake.calls = append(fake.calls, copyArguments)
	if filepath.Base(path) == "git" {
		return commandResult{stdout: []byte(strings.Join(fixturePaths, "\x00") + "\x00")}, nil
	}
	if len(arguments) == 0 || arguments[0] != "--options=NONE" {
		fake.t.Fatalf("Ctags did not disable ambient options first: %q", arguments)
	}
	for _, value := range environment {
		name, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(name, "CTAGS") || strings.EqualFold(name, "ETAGS") {
			fake.t.Fatalf("ambient Ctags environment leaked: %q", value)
		}
	}
	switch {
	case slices.Equal(arguments, []string{"--options=NONE", "--version"}):
		return commandResult{stdout: []byte("Universal Ctags " + fake.version + ", Copyright\n")}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-features"}):
		return commandResult{stdout: []byte(fake.features)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-output-formats"}):
		return commandResult{stdout: []byte(fake.formats)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-languages"}):
		return commandResult{stdout: []byte(fake.languages)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-maps"}):
		return commandResult{stdout: []byte(fake.mappings)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-kinds-full=all"}):
		return commandResult{stdout: []byte(fake.kinds)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-roles=all"}):
		return commandResult{stdout: []byte(fake.roles)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-fields"}):
		return commandResult{stdout: []byte(fake.fields)}, nil
	case slices.Equal(arguments, []string{"--options=NONE", "--list-extras"}):
		return commandResult{stdout: []byte(fake.extras)}, nil
	case slices.Contains(arguments, "--output-format=json"):
		fake.indexDir = directory
		fake.indexArgs = copyArguments
		for _, argument := range arguments {
			if !strings.HasPrefix(argument, "."+string(filepath.Separator)) {
				continue
			}
			if _, err := os.ReadFile(filepath.Join(directory, argument)); err != nil {
				fake.t.Fatalf("Ctags input %q was not snapshotted: %v", argument, err)
			}
		}
		return commandResult{stdout: []byte(fakeCtagsJSON)}, nil
	default:
		return commandResult{}, errors.New("unexpected fake command")
	}
}

const fakeCtagsJSON = `{"_type":"tag","name":"Greeting","path":"./schema.proto","language":"Protobuf","kind":"message","line":5}
{"_type":"tag","name":"greet","path":"./main.lua","language":"Lua","kind":"function","scope":"M","scopeKind":"table","line":3}
{"_type":"tag","name":"greet","path":"./main.lua","language":"Lua","kind":"function","roles":"ref","extras":"reference","line":7}
{"_type":"tag","name":"recent_greetings","path":"./queries.sql","language":"SQL","kind":"view","line":6}
{"_type":"tag","name":"configure_feature","path":"./CMakeLists.txt","language":"CMake","kind":"function","signature":"(target)","line":4}
{"_type":"tag","name":"deploy_app","path":"./deploy.sh","language":"Sh","kind":"function","line":3}
`

func TestDescribeIsDeclarative(t *testing.T) {
	t.Setenv("WEAVE_CTAGS", "")
	t.Setenv("WEAVE_CTAGS_VERSION", "")
	var output bytes.Buffer
	called := false
	err := runCLI(context.Background(), []string{
		"--ctags=/missing/uctags", "--producer-version=9.8.7",
		"describe", "--protocol", adapter.Protocol,
	}, bytes.NewReader(nil), &output, dependencies{
		run: func(context.Context, string, []string, string, []string, outputLimits) (commandResult, error) {
			called = true
			return commandResult{}, nil
		},
		lookPath: func(string) (string, error) {
			called = true
			return "", exec.ErrNotFound
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("describe resolved or ran an external executable")
	}
	var capabilities adapter.Capabilities
	if err := json.Unmarshal(output.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Provider != (adapter.Provider{Name: providerName, Version: "9.8.7"}) {
		t.Fatalf("provider = %#v", capabilities.Provider)
	}
	if !slices.Equal(capabilities.Languages, []string{"*"}) || capabilities.Requires.MayRunBuildTool {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if !slices.Equal(capabilities.Requires.Executables, []string{"git", "Universal Ctags"}) {
		t.Fatalf("requirements = %#v", capabilities.Requires)
	}
}

func TestAdapterLifecycleIsDeterministicAndDefinitionOnly(t *testing.T) {
	t.Setenv("CTAGS", "--options=/hostile/options")
	t.Setenv("ETAGS", "hostile")
	root, err := filepath.Abs(filepath.Join("..", "..", "tests", "fixtures", "mixed"))
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(root)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	run := func() ([]byte, *fakeTools) {
		fake := newFakeTools(t)
		var output bytes.Buffer
		err := runCLI(context.Background(), []string{
			"--ctags=fake-uctags", "index", "--protocol", adapter.Protocol,
		}, bytes.NewReader(encoded), &output, dependencies{run: fake.run, lookPath: fake.lookPath})
		if err != nil {
			t.Fatal(err)
		}
		return output.Bytes(), fake
	}

	first, fake := run()
	second, _ := run()
	if !bytes.Equal(first, second) {
		t.Fatal("identical inputs and producer capabilities emitted different protocol facts")
	}
	if fake.indexDir == root || fake.indexDir == "" {
		t.Fatalf("Ctags directory = %q, want a private snapshot", fake.indexDir)
	}
	if _, err := os.Stat(fake.indexDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private snapshot was not removed: %v", err)
	}
	if !slices.Contains(fake.indexArgs, "--extras=-r") || !slices.Contains(fake.indexArgs, "--sort=no") {
		t.Fatalf("unsafe Ctags index arguments: %q", fake.indexArgs)
	}

	frames := decodeFrames(t, first)
	if frames[0].Kind != "run.begin" || frames[len(frames)-1].Kind != "run.end" {
		t.Fatalf("protocol frame boundary = %q ... %q", frames[0].Kind, frames[len(frames)-1].Kind)
	}
	var documents []graph.Document
	var symbols []graph.Symbol
	var occurrences []graph.Occurrence
	var edges []graph.Edge
	for _, frame := range frames {
		if frame.Kind != "facts" {
			continue
		}
		var facts struct {
			Documents   []graph.Document   `json:"documents"`
			Symbols     []graph.Symbol     `json:"symbols"`
			Occurrences []graph.Occurrence `json:"occurrences"`
			Edges       []graph.Edge       `json:"edges"`
		}
		if err := json.Unmarshal(frame.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		documents = append(documents, facts.Documents...)
		symbols = append(symbols, facts.Symbols...)
		occurrences = append(occurrences, facts.Occurrences...)
		edges = append(edges, facts.Edges...)
	}
	if len(documents) != 6 || len(symbols) != 5 || len(occurrences) != 5 || len(edges) != 0 {
		t.Fatalf("facts = documents %d, symbols %d, occurrences %d, edges %d", len(documents), len(symbols), len(occurrences), len(edges))
	}
	if !slices.ContainsFunc(documents, func(document graph.Document) bool {
		return document.Path == ".gitignore" && document.Language == "unknown"
	}) {
		t.Fatalf("tagless Git-visible file has no document unit: %#v", documents)
	}
	for _, symbol := range symbols {
		if symbol.Evidence != graph.EvidenceSyntactic || symbol.Provider != providerName {
			t.Fatalf("symbol overstates evidence or provider: %#v", symbol)
		}
		if symbol.DisplayName == "should_not_be_indexed" {
			t.Fatal("Git-ignored source was indexed")
		}
	}
	for _, occurrence := range occurrences {
		if occurrence.Role != "definition" || occurrence.Evidence != graph.EvidenceSyntactic {
			t.Fatalf("non-definition occurrence emitted: %#v", occurrence)
		}
	}
}

func TestProducerProbeRejectsIncompatibleHelpers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeTools)
		want   string
	}{
		{"version", func(fake *fakeTools) { fake.version = "5.9.0" }, "want \"6.2.1\""},
		{"json feature", func(fake *fakeTools) { fake.features = "wildcards\n" }, "without JSON support"},
		{"json format", func(fake *fakeTools) { fake.formats = "u-ctags\n" }, "does not advertise JSON"},
		{"languages", func(fake *fakeTools) { fake.languages = "# disabled\n" }, "no language parsers"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTools(t)
			test.mutate(fake)
			_, err := resolveProducer(context.Background(), options{ctags: "fake-uctags", version: defaultVersion}, dependencies{
				run: fake.run, lookPath: fake.lookPath,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestProducerFingerprintIncludesMachineInventories(t *testing.T) {
	first := newFakeTools(t)
	toolA, err := resolveProducer(context.Background(), options{ctags: "fake-uctags", version: defaultVersion}, dependencies{
		run: first.run, lookPath: first.lookPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	second := newFakeTools(t)
	second.mappings += "SQL *.sql\n"
	toolB, err := resolveProducer(context.Background(), options{ctags: "fake-uctags", version: defaultVersion}, dependencies{
		run: second.run, lookPath: second.lookPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if toolA.capabilityDigest == toolB.capabilityDigest {
		t.Fatal("producer capability digest ignored a changed language mapping")
	}
	if len(first.calls) != 9 {
		t.Fatalf("producer probe calls = %d, want version plus eight machine inventories", len(first.calls))
	}
}

func TestGitVisibleFilesHonorsIgnoreRules(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runGit(t, root, git, "init", "--quiet")
	writeTestFile(t, root, ".gitignore", "ignored.lua\n")
	writeTestFile(t, root, "tracked.lua", "function tracked() end\n")
	writeTestFile(t, root, "untracked.sql", "CREATE TABLE example(id INT);\n")
	writeTestFile(t, root, "ignored.lua", "function ignored() end\n")
	runGit(t, root, git, "add", ".gitignore", "tracked.lua")
	paths, err := gitVisibleFiles(context.Background(), root, git, runCommand)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "tracked.lua", "untracked.sql"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}

func TestSnapshotRejectsAParentSymlinkOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mirror := t.TempDir()
	writeTestFile(t, outside, "secret.lua", "function secret() end\n")
	if err := os.Symlink(outside, filepath.Join(root, "jump")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, _, err := snapshotFiles(root, mirror, []string{"jump/secret.lua"})
	if err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("snapshot error = %v, want containment rejection", err)
	}
}

func TestRealUniversalCtagsSmoke(t *testing.T) {
	path := os.Getenv("WEAVE_REAL_CTAGS")
	if path == "" {
		t.Skip("set WEAVE_REAL_CTAGS to opt in")
	}
	versionResult, err := runTool(context.Background(), runCommand, path, []string{"--options=NONE", "--version"}, "", ctagsEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	match := versionPattern.FindSubmatch(versionResult.stdout)
	if len(match) != 2 {
		t.Fatalf("not Universal Ctags: %q", versionResult.stdout)
	}
	configuration := options{ctags: path, version: string(match[1])}
	tool, err := resolveProducer(context.Background(), configuration, dependencies{run: runCommand, lookPath: exec.LookPath, exePath: os.Executable})
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runGit(t, root, git, "init", "--quiet")
	writeTestFile(t, root, "main.lua", "local M = {}\nfunction M.greet(name) return name end\nreturn M\n")
	runGit(t, root, git, "add", "main.lua")
	result, err := index(context.Background(), validRequest(root), tool, git, runCommand)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.units) == 0 || len(result.units[0].Symbols) == 0 {
		t.Fatalf("real producer emitted no definitions: %#v", result.units)
	}
}

type testFrame struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func decodeFrames(t *testing.T, encoded []byte) []testFrame {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var result []testFrame
	for {
		var frame testFrame
		if err := decoder.Decode(&frame); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, frame)
	}
	if len(result) == 0 {
		t.Fatal("adapter emitted no frames")
	}
	return result
}

func validRequest(root string) adapter.IndexRequest {
	return adapter.IndexRequest{
		Protocol: adapter.Protocol, RequestID: "request-ctags", RepositoryRoot: root,
		RepositoryIdentity: "example.test/repository", Limits: adapter.RequestLimits{
			MaxFrameBytes: 1 << 20, MaxTotalBytes: 32 << 20, MaxFrames: 10_000, MaxFacts: 100_000,
		},
	}
}

func runGit(t *testing.T, root, git string, arguments ...string) {
	t.Helper()
	command := exec.Command(git, arguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
