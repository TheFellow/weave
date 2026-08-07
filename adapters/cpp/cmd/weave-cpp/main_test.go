package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type fakeProducer struct {
	calls       [][]string
	directories []string
	temporary   string
	compdb      string
	invoked     bool
}

func (fake *fakeProducer) run(_ context.Context, _ string, arguments []string, directory string) (commandResult, error) {
	fake.calls = append(fake.calls, slices.Clone(arguments))
	fake.directories = append(fake.directories, directory)
	if slices.Equal(arguments, []string{"--version"}) {
		return commandResult{stdout: []byte("scip-clang 0.4.0\nBased on Clang/LLVM fixture\n")}, nil
	}
	fake.invoked = true
	var output string
	for _, argument := range arguments {
		switch {
		case strings.HasPrefix(argument, "--index-output-path="):
			output = strings.TrimPrefix(argument, "--index-output-path=")
		case strings.HasPrefix(argument, "--temporary-output-dir="):
			fake.temporary = strings.TrimPrefix(argument, "--temporary-output-dir=")
		case strings.HasPrefix(argument, "--compdb-path="):
			fake.compdb = strings.TrimPrefix(argument, "--compdb-path=")
		}
	}
	if output == "" {
		return commandResult{}, errors.New("missing output path")
	}
	index, err := proto.Marshal(fixtureIndex())
	if err != nil {
		return commandResult{}, err
	}
	if err := os.WriteFile(output, index, 0o600); err != nil {
		return commandResult{}, err
	}
	return commandResult{stdout: []byte("indexed fixture\n")}, nil
}

func fixtureIndex() *scip.Index {
	return &scip.Index{
		Metadata: &scip.Metadata{
			ToolInfo:             &scip.ToolInfo{Name: "scip-clang", Version: "0.4.0"},
			TextDocumentEncoding: scip.TextEncoding_UTF8,
		},
		Documents: []*scip.Document{{
			RelativePath: "src/geometry.cpp", Language: "cpp",
			Text: "int square(int value) { return value * value; }\n",
			// scip-clang v0.4.0 emits the legacy index shape without the
			// newer per-document position encoding.
			PositionEncoding: scip.PositionEncoding_UnspecifiedPositionEncoding,
			Occurrences: []*scip.Occurrence{{
				TypedRange:  &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{Line: 0, StartCharacter: 4, EndCharacter: 10}},
				Symbol:      "local 0",
				SymbolRoles: int32(scip.SymbolRole_Definition),
			}},
			Symbols: []*scip.SymbolInformation{{
				Symbol: "local 0", DisplayName: "square", Kind: scip.SymbolInformation_Function,
			}},
		}},
	}
}

func TestAdapterLifecycleUsesLiteralArgumentsAndCleansTemporaryFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo;touch-not-a-command")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := []byte("int square(int value) { return value * value; }\n")
	if err := os.WriteFile(filepath.Join(root, "src", "geometry.cpp"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	compdb := filepath.Join(root, "compile_commands.json")
	if err := os.WriteFile(compdb, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "add", "compile_commands.json")
	root, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	compdb = filepath.Join(root, "compile_commands.json")

	fake := new(fakeProducer)
	request := validRequest(root)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var diagnostics bytes.Buffer
	err = runCLI(context.Background(), []string{
		"--scip-clang=" + os.Args[0], "index", "--protocol", adapter.Protocol,
	}, bytes.NewReader(encoded), &output, &diagnostics, dependencies{run: fake.run})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.invoked || fake.compdb != compdb || fake.directories[len(fake.directories)-1] != root {
		t.Fatalf("producer invocation = calls %#v, compdb %q, dirs %#v", fake.calls, fake.compdb, fake.directories)
	}
	if _, err := os.Stat(fake.temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private temporary directory still exists: %q, %v", fake.temporary, err)
	}
	if _, err := os.Stat(filepath.Join(root, "index.scip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter wrote index into repository: %v", err)
	}
	if diagnostics.String() != "indexed fixture\n" {
		t.Fatalf("producer stdout was not routed to bounded diagnostics: %q", diagnostics.String())
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) < 5 {
		t.Fatalf("adapter emitted only %d frames: %s", len(lines), output.String())
	}
	var first, last struct {
		Protocol string `json:"protocol"`
		Kind     string `json:"kind"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatal(err)
	}
	if first.Protocol != adapter.Protocol || first.Kind != "run.begin" || last.Kind != "run.end" {
		t.Fatalf("invalid lifecycle boundaries: %#v / %#v", first, last)
	}
}

func TestPermissionDenialHappensBeforeIndexing(t *testing.T) {
	root := t.TempDir()
	request := validRequest(root)
	request.Permissions.BuildTool = false
	encoded, _ := json.Marshal(request)
	fake := new(fakeProducer)
	err := runCLI(context.Background(), []string{
		"--scip-clang=" + os.Args[0], "index", "--protocol", adapter.Protocol,
	}, bytes.NewReader(encoded), &bytes.Buffer{}, &bytes.Buffer{}, dependencies{run: fake.run})
	if err == nil || !strings.Contains(err.Error(), "build_tool permission") {
		t.Fatalf("permission error = %v", err)
	}
	if fake.invoked || len(fake.calls) != 1 || !slices.Equal(fake.calls[0], []string{"--version"}) {
		t.Fatalf("indexer ran without permission: %#v", fake.calls)
	}
}

func TestCompilationDatabaseSelectionIsDeterministicAndContained(t *testing.T) {
	root, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "a", "compile_commands.json")
	second := filepath.Join(root, "b", "compile_commands.json")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "add", "a/compile_commands.json", "b/compile_commands.json")
	if _, err := selectCompilationDatabase(context.Background(), root, ""); err == nil || !strings.Contains(err.Error(), "multiple") || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("ambiguous selection error = %v", err)
	}
	got, err := selectCompilationDatabase(context.Background(), root, filepath.Join("b", "compile_commands.json"))
	if err != nil || got != second {
		t.Fatalf("explicit selection = %q, %v; want %q", got, err, second)
	}
	outside := filepath.Join(t.TempDir(), "compile_commands.json")
	if err := os.WriteFile(outside, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := selectCompilationDatabase(context.Background(), root, outside); err == nil || !strings.Contains(err.Error(), "inside repository") {
		t.Fatalf("outside selection error = %v", err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "compile_commands.json")
		if err := os.Symlink(second, link); err != nil {
			t.Fatal(err)
		}
		if _, err := selectCompilationDatabase(context.Background(), root, link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlink selection error = %v", err)
		}
	}
}

func TestIgnoredCompilationDatabaseRequiresExplicitSelection(t *testing.T) {
	root, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(root, "build")
	if err := os.MkdirAll(build, 0o755); err != nil {
		t.Fatal(err)
	}
	compdb := filepath.Join(build, "compile_commands.json")
	if err := os.WriteFile(compdb, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "add", ".gitignore")
	if _, err := selectCompilationDatabase(context.Background(), root, ""); err == nil || !strings.Contains(err.Error(), "Git-visible") {
		t.Fatalf("ignored implicit database error = %v", err)
	}
	got, err := selectCompilationDatabase(context.Background(), root, "build/compile_commands.json")
	if err != nil || got != compdb {
		t.Fatalf("explicit ignored database = %q, %v; want %q", got, err, compdb)
	}
}

func TestDescribeAndStrictRequestValidation(t *testing.T) {
	fake := new(fakeProducer)
	var output bytes.Buffer
	err := runCLI(context.Background(), []string{
		"--scip-clang=" + os.Args[0], "describe", "--protocol", adapter.Protocol,
	}, bytes.NewReader(nil), &output, &bytes.Buffer{}, dependencies{run: fake.run})
	if err != nil {
		t.Fatal(err)
	}
	var capabilities adapter.Capabilities
	if err := json.Unmarshal(output.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Provider.Name != providerName || capabilities.Provider.Version != "0.4.0" || !capabilities.Requires.MayRunBuildTool || !slices.Equal(capabilities.Languages, []string{"c", "cpp", "cuda"}) {
		t.Fatalf("capabilities = %#v", capabilities)
	}

	root := t.TempDir()
	request := validRequest(root)
	encoded, _ := json.Marshal(request)
	encoded = bytes.Replace(encoded, []byte(`"request_id":"cpp-test"`), []byte(`"request_id":"cpp-test","surprise":true`), 1)
	if _, err := readRequest(bytes.NewReader(encoded)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field error = %v", err)
	}
}

func validRequest(root string) adapter.IndexRequest {
	return adapter.IndexRequest{
		Protocol: adapter.Protocol, RequestID: "cpp-test", RepositoryRoot: root,
		RepositoryIdentity: "example.test/cpp",
		Permissions:        adapter.Permissions{BuildTool: true},
		Limits: adapter.RequestLimits{
			MaxFrameBytes: 4096, MaxTotalBytes: 1 << 20, MaxFrames: 1000, MaxFacts: 1000,
		},
	}
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
