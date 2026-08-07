package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type fakeProducer struct {
	arguments []string
	directory string
	output    string
	temporary string
	version   string
	invoked   bool
	err       error
}

func (fake *fakeProducer) run(_ context.Context, _ string, arguments []string, directory string) (commandResult, error) {
	fake.invoked = true
	fake.arguments = slices.Clone(arguments)
	fake.directory = directory
	for _, argument := range arguments {
		switch {
		case strings.HasPrefix(argument, "--output="):
			fake.output = strings.TrimPrefix(argument, "--output=")
		case strings.HasPrefix(argument, "--temporary-directory="):
			fake.temporary = strings.TrimPrefix(argument, "--temporary-directory=")
		}
	}
	if fake.err != nil {
		return commandResult{stderr: []byte("producer failed\n")}, fake.err
	}
	if fake.output == "" {
		return commandResult{}, errors.New("missing output path")
	}
	version := fake.version
	if version == "" {
		version = defaultMetadataVersion
	}
	encoded, err := proto.Marshal(fixtureIndex(version))
	if err != nil {
		return commandResult{}, err
	}
	if err := os.WriteFile(fake.output, encoded, 0o600); err != nil {
		return commandResult{}, err
	}
	return commandResult{stdout: []byte("indexed Java and Kotlin fixture\n")}, nil
}

func fixtureIndex(version string) *scip.Index {
	greeter := "scip-java maven example 1.0.0 example/Greeter#"
	friendly := "scip-java maven example 1.0.0 example/FriendlyGreeter#"
	return &scip.Index{
		Metadata: &scip.Metadata{
			ToolInfo:             &scip.ToolInfo{Name: "scip-java", Version: version},
			TextDocumentEncoding: scip.TextEncoding_UTF8,
		},
		Documents: []*scip.Document{
			{
				RelativePath: "src/main/java/example/Greeter.java",
				Language:     "java",
				Occurrences: []*scip.Occurrence{{
					TypedRange:  singleLineRange(2, 17, 24),
					Symbol:      greeter,
					SymbolRoles: int32(scip.SymbolRole_Definition),
				}},
				Symbols: []*scip.SymbolInformation{{
					Symbol: greeter, DisplayName: "Greeter", Kind: scip.SymbolInformation_Interface,
				}},
			},
			{
				RelativePath: "src/main/kotlin/example/FriendlyGreeter.kt",
				Language:     "kotlin",
				Occurrences: []*scip.Occurrence{{
					TypedRange:  singleLineRange(2, 6, 21),
					Symbol:      friendly,
					SymbolRoles: int32(scip.SymbolRole_Definition),
				}},
				Symbols: []*scip.SymbolInformation{{
					Symbol: friendly, DisplayName: "FriendlyGreeter", Kind: scip.SymbolInformation_Class,
					Relationships: []*scip.Relationship{{Symbol: greeter, IsImplementation: true}},
				}},
			},
		},
	}
}

func singleLineRange(line, start, end int32) *scip.Occurrence_SingleLineRange {
	return &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{
		Line: line, StartCharacter: start, EndCharacter: end,
	}}
}

func TestDescribeIsDeclarativeAndDoesNotResolveJava(t *testing.T) {
	var output bytes.Buffer
	runnerCalled := false
	err := runCLI(context.Background(), []string{
		"--scip-java=/definitely/missing/scip-java",
		"--producer-version=9.8.7",
		"describe", "--protocol", adapter.Protocol,
	}, bytes.NewReader(nil), &output, &bytes.Buffer{}, dependencies{run: func(context.Context, string, []string, string) (commandResult, error) {
		runnerCalled = true
		return commandResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if runnerCalled {
		t.Fatal("describe invoked the producer")
	}
	var capabilities adapter.Capabilities
	if err := json.Unmarshal(output.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Provider.Name != providerName || capabilities.Provider.Version != "9.8.7" {
		t.Fatalf("provider = %#v", capabilities.Provider)
	}
	if !slices.Equal(capabilities.Languages, []string{"java", "kotlin"}) || !capabilities.Requires.MayRunBuildTool {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if !slices.Equal(capabilities.Requires.Executables, []string{"scip-java"}) {
		t.Fatalf("requirements = %#v", capabilities.Requires)
	}
}

func TestAdapterLifecycleUsesLiteralArgumentsAndLegacyUTF16Ranges(t *testing.T) {
	root := copyFixture(t)
	fake := new(fakeProducer)
	request := validRequest(root)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	err = runCLI(context.Background(), []string{
		"--scip-java=" + os.Args[0], "--build-tool=gradle",
		"index", "--protocol", adapter.Protocol,
	}, bytes.NewReader(encoded), &output, &diagnostics, dependencies{run: fake.run})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.invoked || fake.directory != root {
		t.Fatalf("producer invocation = %v, directory %q", fake.invoked, fake.directory)
	}
	wantPrefix := []string{"index", "--cwd=" + root}
	if len(fake.arguments) != 6 || !slices.Equal(fake.arguments[:2], wantPrefix) || fake.arguments[4] != "--no-bazel-autorun-sandbox-command" || fake.arguments[5] != "--build-tool=gradle" {
		t.Fatalf("literal producer arguments = %#v", fake.arguments)
	}
	if filepath.Dir(fake.output) != filepath.Dir(fake.temporary) || filepath.Base(fake.output) != "index.scip" {
		t.Fatalf("private output = %q, temporary = %q", fake.output, fake.temporary)
	}
	if _, err := os.Stat(filepath.Dir(fake.output)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private adapter directory still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "index.scip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter wrote index.scip into the repository: %v", err)
	}
	if diagnostics.String() != "indexed Java and Kotlin fixture\n" {
		t.Fatalf("diagnostics = %q", diagnostics.String())
	}
	text := output.String()
	for _, fragment := range []string{
		`"kind":"run.begin"`, `"kind":"run.end"`,
		`"language":"java"`, `"language":"kotlin"`,
		`"provider_version":"0.13.1"`,
		`"display_name":"Greeter"`, `"display_name":"FriendlyGreeter"`,
		`"kind":"implements"`, `"evidence":"exact"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("protocol output missing %q:\n%s", fragment, text)
		}
	}
}

func TestEveryRepositoryBuildCapabilityIsDeniedBeforeProducerDiscovery(t *testing.T) {
	permissions := adapter.Permissions{BuildTool: true, Restore: true, Network: true, RunGenerators: true}
	tests := []struct {
		name string
		deny func(*adapter.Permissions)
		want string
	}{
		{"build tool", func(value *adapter.Permissions) { value.BuildTool = false }, "build_tool"},
		{"restore", func(value *adapter.Permissions) { value.Restore = false }, "restore"},
		{"network", func(value *adapter.Permissions) { value.Network = false }, "network"},
		{"generators", func(value *adapter.Permissions) { value.RunGenerators = false }, "run_generators"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest(t.TempDir())
			request.Permissions = permissions
			test.deny(&request.Permissions)
			encoded, _ := json.Marshal(request)
			invoked := false
			err := runCLI(context.Background(), []string{
				"--scip-java=/definitely/missing/scip-java", "index", "--protocol", adapter.Protocol,
			}, bytes.NewReader(encoded), &bytes.Buffer{}, &bytes.Buffer{}, dependencies{run: func(context.Context, string, []string, string) (commandResult, error) {
				invoked = true
				return commandResult{}, nil
			}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("permission error = %v", err)
			}
			if invoked {
				t.Fatal("producer ran without permission")
			}
		})
	}
}

func TestEmbeddedProducerMetadataMustMatchPinnedContract(t *testing.T) {
	root := copyFixture(t)
	request := validRequest(root)
	encoded, _ := json.Marshal(request)
	fake := &fakeProducer{version: "0.12.0"}
	var output bytes.Buffer
	err := runCLI(context.Background(), []string{
		"--scip-java=" + os.Args[0], "index", "--protocol", adapter.Protocol,
	}, bytes.NewReader(encoded), &output, &bytes.Buffer{}, dependencies{run: fake.run})
	if err == nil || !strings.Contains(err.Error(), "metadata-version") || !strings.Contains(err.Error(), "0.12.0") {
		t.Fatalf("metadata mismatch error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("facts emitted before metadata validation: %s", output.String())
	}
	if _, statErr := os.Stat(filepath.Dir(fake.output)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("private adapter directory still exists: %v", statErr)
	}
}

func TestArgumentsAndRequestsAreStrict(t *testing.T) {
	for _, arguments := range [][]string{
		{"describe", "--protocol", "wrong"},
		{"--producer-version=not a version", "describe", "--protocol", adapter.Protocol},
		{"--metadata-version=not a version", "describe", "--protocol", adapter.Protocol},
		{"--build-tool=ant", "describe", "--protocol", adapter.Protocol},
		{"--scip-java=a", "--scip-java=b", "describe", "--protocol", adapter.Protocol},
	} {
		if _, _, err := parseArguments(arguments); err == nil {
			t.Fatalf("arguments accepted: %#v", arguments)
		}
	}
	request := validRequest(t.TempDir())
	encoded, _ := json.Marshal(request)
	encoded = bytes.Replace(encoded, []byte(`"request_id":"jvm-test"`), []byte(`"request_id":"jvm-test","surprise":true`), 1)
	if _, err := readRequest(bytes.NewReader(encoded)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field error = %v", err)
	}
}

func TestProducerOutputAndProtocolFramesAreBounded(t *testing.T) {
	buffer := cappedBuffer{limit: 3}
	if _, err := buffer.Write([]byte("four")); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("producer output bound error = %v", err)
	}

	var output bytes.Buffer
	writer := frameWriter{
		output: &output, requestID: "bounded",
		limits: adapter.RequestLimits{MaxFrameBytes: 128, MaxTotalBytes: 256, MaxFrames: 1},
	}
	if err := writer.emit("first", map[string]string{"value": "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.emit("second", map[string]string{"value": "no"}); err == nil || !strings.Contains(err.Error(), "max_frames") {
		t.Fatalf("frame count bound error = %v", err)
	}

	tiny := frameWriter{
		output: &bytes.Buffer{}, requestID: "bounded",
		limits: adapter.RequestLimits{MaxFrameBytes: 32, MaxTotalBytes: 256, MaxFrames: 1},
	}
	if err := tiny.emit("oversized", map[string]string{"value": strings.Repeat("x", 64)}); err == nil || !strings.Contains(err.Error(), "max_frame_bytes") {
		t.Fatalf("frame byte bound error = %v", err)
	}
}

func validRequest(root string) adapter.IndexRequest {
	return adapter.IndexRequest{
		Protocol: adapter.Protocol, RequestID: "jvm-test", RepositoryRoot: root,
		RepositoryIdentity: "example.test/jvm",
		Permissions: adapter.Permissions{
			BuildTool: true, Restore: true, Network: true, RunGenerators: true,
		},
		Limits: adapter.RequestLimits{
			MaxFrameBytes: 4096, MaxTotalBytes: 1 << 20, MaxFrames: 1000, MaxFacts: 1000,
		},
	}
}

func copyFixture(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", "..", "tests", "fixtures", "mixed"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "repo;not-a-command")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	root, err = canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
