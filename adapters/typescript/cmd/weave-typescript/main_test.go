package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

const helperEnvironment = "WEAVE_TYPESCRIPT_TEST_PRODUCER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnvironment) == "1" {
		os.Exit(runProducerHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runProducerHelper(arguments []string) int {
	if slices.Equal(arguments, []string{"--version"}) {
		fmt.Println(supportedVersion)
		return 0
	}
	if len(arguments) == 0 || arguments[0] != "index" {
		return 91
	}
	var output string
	for index, argument := range arguments {
		if argument == "--output" && index+1 < len(arguments) {
			output = arguments[index+1]
		}
	}
	if output == "" {
		return 92
	}
	encoded, err := proto.Marshal(fixtureIndex())
	if err != nil {
		return 93
	}
	if err := os.WriteFile(output, encoded, 0o600); err != nil {
		return 94
	}
	fmt.Println("indexed fixture")
	return 0
}

type fakeProducer struct {
	calls       [][]string
	directories []string
	output      string
	invoked     bool
}

func (fake *fakeProducer) run(_ context.Context, _ string, arguments []string, directory string) (commandResult, error) {
	fake.calls = append(fake.calls, slices.Clone(arguments))
	fake.directories = append(fake.directories, directory)
	if slices.Equal(arguments, []string{"--version"}) {
		return commandResult{stdout: []byte(supportedVersion + "\n")}, nil
	}
	fake.invoked = true
	for index, argument := range arguments {
		if argument == "--output" && index+1 < len(arguments) {
			fake.output = arguments[index+1]
		}
	}
	if fake.output == "" {
		return commandResult{}, errors.New("missing output path")
	}
	encoded, err := proto.Marshal(fixtureIndex())
	if err != nil {
		return commandResult{}, err
	}
	if err := os.WriteFile(fake.output, encoded, 0o600); err != nil {
		return commandResult{}, err
	}
	return commandResult{stdout: []byte("indexed fixture\n")}, nil
}

func fixtureIndex() *scip.Index {
	type sourceFixture struct {
		path, source, symbol, display string
		start                         int
	}
	typescriptSource := `const face = "😀"; export function greet(name: string) { return name }` + "\n"
	fixtures := []sourceFixture{
		{"src/service.ts", typescriptSource, "local 0", "greet", utf16Column(typescriptSource, strings.Index(typescriptSource, "greet"))},
		{"src/view.tsx", "export const View = () => <div />\n", "local 0", "View", 13},
		{"src/legacy.js", "export function legacy(value) { return value }\n", "local 0", "legacy", 16},
		{"src/widget.jsx", "export const Widget = () => <section />\n", "local 0", "Widget", 13},
	}
	index := &scip.Index{Metadata: &scip.Metadata{
		ToolInfo:             &scip.ToolInfo{Name: "scip-typescript", Version: supportedVersion},
		TextDocumentEncoding: scip.TextEncoding_UTF8,
	}}
	for _, fixture := range fixtures {
		index.Documents = append(index.Documents, &scip.Document{
			RelativePath: fixture.path,
			Text:         fixture.source,
			Occurrences: []*scip.Occurrence{{
				TypedRange: &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{
					Line: 0, StartCharacter: int32(fixture.start), EndCharacter: int32(fixture.start + len(fixture.display)),
				}},
				Symbol: fixture.symbol, SymbolRoles: int32(scip.SymbolRole_Definition),
			}},
			Symbols: []*scip.SymbolInformation{{
				Symbol: fixture.symbol, DisplayName: fixture.display, Kind: scip.SymbolInformation_Function,
			}},
		})
	}
	return index
}

func utf16Column(source string, byteOffset int) int {
	return len(utf16.Encode([]rune(source[:byteOffset])))
}

func TestAdapterLifecycleUsesLiteralArgumentsAndLegacyUTF16Ranges(t *testing.T) {
	root := createFixtureRepository(t, "repo;not-a-command")
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	fake := new(fakeProducer)
	request := validRequest(canonicalRoot)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var diagnostics bytes.Buffer
	err = runCLI(context.Background(), []string{
		"--scip-typescript=" + os.Args[0], "index", "--protocol", adapter.Protocol,
	}, bytes.NewReader(encoded), &output, &diagnostics, dependencies{run: fake.run})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.invoked || fake.directories[len(fake.directories)-1] != canonicalRoot {
		t.Fatalf("producer invocation = calls %#v, dirs %#v", fake.calls, fake.directories)
	}
	wantArguments := []string{
		"index", "--cwd", canonicalRoot, "--output", fake.output,
		"--no-progress-bar", "--no-global-caches", filepath.Join(canonicalRoot, "tsconfig.json"),
	}
	if !slices.Equal(fake.calls[len(fake.calls)-1], wantArguments) {
		t.Fatalf("producer arguments = %#v, want %#v", fake.calls[len(fake.calls)-1], wantArguments)
	}
	if slices.Contains(fake.calls[len(fake.calls)-1], "--infer-tsconfig") {
		t.Fatal("unsafe upstream config inference was enabled")
	}
	if _, err := os.Stat(filepath.Dir(fake.output)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private temporary directory still exists: %q, %v", filepath.Dir(fake.output), err)
	}
	if _, err := os.Stat(filepath.Join(canonicalRoot, "index.scip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter wrote index into repository: %v", err)
	}
	if diagnostics.String() != "indexed fixture\n" {
		t.Fatalf("producer stdout was not routed to diagnostics: %q", diagnostics.String())
	}

	documents, occurrences, firstKind, lastKind := decodeFrames(t, output.Bytes())
	wantLanguages := []string{"javascript", "javascriptreact", "typescript", "typescriptreact"}
	slices.Sort(documents)
	if !slices.Equal(documents, wantLanguages) {
		t.Fatalf("document languages = %#v, want %#v", documents, wantLanguages)
	}
	if firstKind != "run.begin" || lastKind != "run.end" {
		t.Fatalf("lifecycle boundaries = %q / %q", firstKind, lastKind)
	}
	// "greet" follows a non-BMP rune. Its TypeScript UTF-16 column is 35,
	// while Weave correctly reports byte column 37 after normalization.
	if !slices.Contains(occurrences, int32(37)) {
		t.Fatalf("occurrence byte columns = %#v; UTF-16 range was not normalized", occurrences)
	}
}

func decodeFrames(t *testing.T, encoded []byte) ([]string, []int32, string, string) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(encoded), []byte("\n"))
	var languages []string
	var columns []int32
	var first, last string
	for index, line := range lines {
		var frame struct {
			Kind    string `json:"kind"`
			Payload struct {
				Documents []struct {
					Language string `json:"language"`
				} `json:"documents"`
				Occurrences []struct {
					Range struct {
						Start struct {
							Column int32 `json:"column"`
						} `json:"start"`
					} `json:"range"`
				} `json:"occurrences"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("decode frame %d: %v", index, err)
		}
		if index == 0 {
			first = frame.Kind
		}
		last = frame.Kind
		for _, document := range frame.Payload.Documents {
			languages = append(languages, document.Language)
		}
		for _, occurrence := range frame.Payload.Occurrences {
			columns = append(columns, occurrence.Range.Start.Column)
		}
	}
	return languages, columns, first, last
}

func TestRealSubprocessImplementsAdapterContract(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "weave-typescript")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command("go", "build", "-o", executable, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, output)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := createFixtureRepository(t, "process-project")
	result, err := (adapter.Runner{}).Index(context.Background(), adapter.Executable{
		Path: executable,
		Env: append(os.Environ(),
			helperEnvironment+"=1",
			"WEAVE_SCIP_TYPESCRIPT="+helper,
		),
	}, adapter.IndexRequest{
		RequestID: "typescript-process", RepositoryRoot: root,
		RepositoryIdentity: "example.test/typescript",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider.Name != providerName || result.Provider.Version != supportedVersion || len(result.Units) != 4 {
		t.Fatalf("process result = %#v", result)
	}
	if !strings.Contains(result.Stderr, "indexed fixture") {
		t.Fatalf("producer diagnostics = %q", result.Stderr)
	}
}

func TestProjectSelectionIsContainedAndNeverInfers(t *testing.T) {
	root, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	jsconfig := filepath.Join(root, "jsconfig.json")
	if err := os.WriteFile(jsconfig, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := selectProject(root, "")
	if err != nil || got != jsconfig {
		t.Fatalf("jsconfig selection = %q, %v", got, err)
	}
	if err := os.Remove(jsconfig); err != nil {
		t.Fatal(err)
	}
	if _, err := selectProject(root, ""); err == nil || !strings.Contains(err.Error(), "intentionally disabled") {
		t.Fatalf("missing config error = %v", err)
	}

	outside := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := selectProject(root, outside); err == nil || !strings.Contains(err.Error(), "inside repository") {
		t.Fatalf("outside project error = %v", err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "tsconfig.json")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := selectProject(root, ""); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlink project error = %v", err)
		}
	}
}

func TestDescribeStrictArgumentsAndProducerMetadata(t *testing.T) {
	fake := new(fakeProducer)
	var output bytes.Buffer
	err := runCLI(context.Background(), []string{
		"--scip-typescript=" + os.Args[0], "describe", "--protocol", adapter.Protocol,
	}, bytes.NewReader(nil), &output, &bytes.Buffer{}, dependencies{run: fake.run})
	if err != nil {
		t.Fatal(err)
	}
	var capabilities adapter.Capabilities
	if err := json.Unmarshal(output.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	wantLanguages := []string{"javascript", "javascriptreact", "typescript", "typescriptreact"}
	if capabilities.Provider.Name != providerName || capabilities.Provider.Version != supportedVersion || capabilities.Requires.MayRunBuildTool || !slices.Equal(capabilities.Languages, wantLanguages) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if _, _, err := parseArguments([]string{"--project=a", "--project=b", "index", "--protocol", adapter.Protocol}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate argument error = %v", err)
	}

	root := createFixtureRepository(t, "strict-request")
	request := validRequest(root)
	encoded, _ := json.Marshal(request)
	encoded = bytes.Replace(encoded, []byte(`"request_id":"typescript-test"`), []byte(`"request_id":"typescript-test","surprise":true`), 1)
	if _, err := readRequest(bytes.NewReader(encoded)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field error = %v", err)
	}

	invalid := fixtureIndex()
	invalid.Metadata.ToolInfo.Version = "0.5.0"
	data, err := proto.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareIndex(data, false); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("producer metadata error = %v", err)
	}
}

func TestWindowsProducerPathsAndLanguagesAreNormalized(t *testing.T) {
	index := fixtureIndex()
	index.Documents[0].RelativePath = `src\service.ts`
	encoded, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareIndex(encoded, true)
	if err != nil {
		t.Fatal(err)
	}
	var got scip.Index
	if err := proto.Unmarshal(prepared, &got); err != nil {
		t.Fatal(err)
	}
	if got.Documents[0].RelativePath != "src/service.ts" || got.Documents[0].Language != "typescript" {
		t.Fatalf("normalized document = %#v", got.Documents[0])
	}
}

func createFixtureRepository(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, document := range fixtureIndex().Documents {
		target := filepath.Join(root, filepath.FromSlash(document.RelativePath))
		if err := os.WriteFile(target, []byte(document.Text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := `{"compilerOptions":{"allowJs":true,"jsx":"preserve","noEmit":true},"include":["src/**/*"]}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func validRequest(root string) adapter.IndexRequest {
	return adapter.IndexRequest{
		Protocol: adapter.Protocol, RequestID: "typescript-test", RepositoryRoot: root,
		RepositoryIdentity: "example.test/typescript",
		Limits: adapter.RequestLimits{
			MaxFrameBytes: 4096, MaxTotalBytes: 1 << 20, MaxFrames: 1000, MaxFacts: 1000,
		},
	}
}
