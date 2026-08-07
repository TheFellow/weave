package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/graph"
)

func TestRunnerAcceptsMultiFrameRunAndKeepsStderrSeparate(t *testing.T) {
	t.Parallel()
	runner := Runner{}
	result, err := runner.Index(context.Background(), helperExecutable("success"), IndexRequest{
		RequestID: "request-1", RepositoryRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Units) != 1 || len(result.Units[0].Documents) != 1 || len(result.Units[0].Symbols) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Provider.Name != "fixture-adapter" || result.Diagnostics[0].Message != "indexed fixture" {
		t.Fatalf("metadata = %#v", result)
	}
	if result.Stderr != "operator note" {
		t.Fatalf("stderr = %q", result.Stderr)
	}
}

func TestRunnerRejectsMalformedOrIncompleteRuns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		scenario string
		limits   Limits
		contains string
	}{
		{"truncated", "truncated", Limits{}, "before run.end"},
		{"version mismatch", "version", Limits{}, "mismatched protocol"},
		{"invalid json", "invalid-json", Limits{}, "invalid character"},
		{"duplicate record", "duplicate", Limits{}, "duplicate id"},
		{"unknown frame", "unknown", Limits{}, "unknown frame kind"},
		{"deep json", "deep-json", Limits{}, "nesting exceeds"},
		{"nonzero exit", "nonzero", Limits{}, "exit status"},
		{"oversized frame", "oversized", Limits{MaxFrameBytes: 512, MaxTotalBytes: 4096}, "frame exceeds"},
		{"oversized stderr", "stderr-overflow", Limits{MaxStderrBytes: 16}, "read stderr"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := (Runner{Limits: test.limits}).Index(context.Background(), helperExecutable(test.scenario), IndexRequest{
				RequestID: "request-1", RepositoryRoot: t.TempDir(),
			})
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Index() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestRunnerCancelsTimedOutAdapter(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (Runner{Limits: Limits{WaitDelay: 50 * time.Millisecond}}).Index(ctx, helperExecutable("timeout"), IndexRequest{
		RequestID: "request-1", RepositoryRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("Index() error = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("cancellation took %v", time.Since(started))
	}
}

func TestDescribeRejectsUnsupportedCapabilities(t *testing.T) {
	t.Parallel()
	_, _, err := (Runner{}).Describe(context.Background(), helperExecutable("unsupported"))
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("Describe() error = %v", err)
	}
}

func TestDotnetCapabilityFixtureMatchesProtocol(t *testing.T) {
	t.Parallel()
	encoded, err := os.ReadFile("testdata/weave-dotnet.describe.json")
	if err != nil {
		t.Fatal(err)
	}
	var capabilities Capabilities
	if err := decodeStrict(encoded, &capabilities); err != nil {
		t.Fatal(err)
	}
	capabilities, err = NormalizeCapabilities(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Provider != (Provider{Name: "weave-dotnet", Version: "0.1.0"}) ||
		!slices.Equal(capabilities.Languages, []string{"csharp", "fsharp"}) ||
		!capabilities.Requires.MayRunBuildTool || !slices.Contains(capabilities.Requires.Executables, "dotnet") {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestPublishedV0ContractFixtures(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "protocol", "adapter", "v0")

	encoded, err := os.ReadFile(filepath.Join(root, "describe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capabilities Capabilities
	if err := decodeStrict(encoded, &capabilities); err != nil {
		t.Fatalf("decode published capabilities: %v", err)
	}
	if capabilities.Provider != (Provider{Name: "fixture-adapter", Version: "1.0.0"}) ||
		!slices.Contains(capabilities.Protocols, Protocol) || capabilities.FactEncoding != FactEncoding {
		t.Fatalf("published capabilities = %#v", capabilities)
	}

	encoded, err = os.ReadFile(filepath.Join(root, "index-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request IndexRequest
	if err := decodeStrict(encoded, &request); err != nil {
		t.Fatalf("decode published request: %v", err)
	}
	if request.Protocol != Protocol || request.RequestID != "fixture-request" || request.RepositoryRoot == "" {
		t.Fatalf("published request = %#v", request)
	}

	response, err := os.Open(filepath.Join(root, "index-response.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	result, parseErr := parseFrames(response, request.RequestID, capabilities, (Limits{}).withDefaults())
	closeErr := response.Close()
	if parseErr != nil {
		t.Fatalf("parse published response: %v", parseErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(result.Units) != 1 || len(result.Units[0].Documents) != 1 || len(result.Units[0].Symbols) != 1 ||
		len(result.Diagnostics) != 1 {
		t.Fatalf("published response = %#v", result)
	}

	malformed, err := os.Open(filepath.Join(root, "malformed-truncated.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	_, parseErr = parseFrames(malformed, request.RequestID, capabilities, (Limits{}).withDefaults())
	closeErr = malformed.Close()
	if parseErr == nil || !strings.Contains(parseErr.Error(), "before run.end") {
		t.Fatalf("parse malformed published response error = %v", parseErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestPythonAdapterImplementsPublishedProcessContract(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python is not installed")
	}
	root := t.TempDir()
	command := exec.Command("git", "init", "--quiet")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "example.py"), []byte("def greet(name):\n    return name.strip()\n\ndef run():\n    return greet('weave')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", "..", "adapters", "python", "src"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Runner{}).Index(context.Background(), Executable{
		Path: python, Args: []string{"-m", "weave_python"}, Dir: root,
		Env: append(os.Environ(), "PYTHONPATH="+moduleRoot),
	}, IndexRequest{RequestID: "python-contract", RepositoryRoot: root, RepositoryIdentity: "example.com/python"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider.Name != "weave-python" || len(result.Units) != 1 {
		t.Fatalf("Python adapter result = %#v", result)
	}
	var exactReference, syntacticCall bool
	for _, edge := range result.Units[0].Edges {
		exactReference = exactReference || edge.Kind == graph.EdgeReferences && edge.Evidence == graph.EvidenceExact
		syntacticCall = syntacticCall || edge.Kind == graph.EdgeCalls && edge.Evidence == graph.EvidenceSyntactic
	}
	if !exactReference || !syntacticCall {
		t.Fatalf("Python adapter evidence = %#v", result.Units[0].Edges)
	}
}

func helperExecutable(scenario string) Executable {
	return Executable{
		Path: os.Args[0], Args: []string{"-test.run=TestAdapterHelperProcess", "--"},
		Env: append(os.Environ(), "WEAVE_ADAPTER_HELPER=1", "WEAVE_ADAPTER_SCENARIO="+scenario),
	}
}

func TestAdapterHelperProcess(t *testing.T) {
	if os.Getenv("WEAVE_ADAPTER_HELPER") != "1" {
		return
	}
	separator := -1
	for i, argument := range os.Args {
		if argument == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(90)
	}
	operation := os.Args[separator+1]
	scenario := os.Getenv("WEAVE_ADAPTER_SCENARIO")
	if operation == "describe" {
		capabilities := fixtureCapabilities()
		if scenario == "unsupported" {
			capabilities.Protocols = []string{"weave.adapter/v99"}
		}
		_ = json.NewEncoder(os.Stdout).Encode(capabilities)
		os.Exit(0)
	}
	if operation != "index" {
		os.Exit(91)
	}
	var request IndexRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(92)
	}
	switch scenario {
	case "timeout":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "nonzero":
		fmt.Fprintln(os.Stderr, "producer failed")
		os.Exit(7)
	case "invalid-json":
		fmt.Fprintln(os.Stdout, "{not-json}")
		os.Exit(0)
	case "oversized":
		fmt.Fprintf(os.Stdout, "{\"protocol\":\"%s\",\"request_id\":\"%s\",\"kind\":\"unknown\",\"payload\":{\"padding\":\"%s\"}}\n", Protocol, request.RequestID, strings.Repeat("x", 1000))
		os.Exit(0)
	case "stderr-overflow":
		fmt.Fprintln(os.Stderr, strings.Repeat("x", 1000))
		writeValidRun(request.RequestID, false)
		os.Exit(0)
	case "version":
		writeFrame("weave.adapter/v99", request.RequestID, "run.begin", runBegin{fixtureCapabilities().Provider, FactEncoding})
		os.Exit(0)
	case "truncated":
		writeFrame(Protocol, request.RequestID, "run.begin", runBegin{fixtureCapabilities().Provider, FactEncoding})
		os.Exit(0)
	case "unknown":
		writeFrame(Protocol, request.RequestID, "run.begin", runBegin{fixtureCapabilities().Provider, FactEncoding})
		writeFrame(Protocol, request.RequestID, "mystery", struct{}{})
		os.Exit(0)
	case "deep-json":
		fmt.Fprintf(os.Stdout, "{\"protocol\":\"%s\",\"request_id\":\"%s\",\"kind\":\"run.begin\",\"payload\":%s}\n", Protocol, request.RequestID, strings.Repeat("[", 101)+strings.Repeat("]", 101))
		os.Exit(0)
	case "duplicate":
		writeDuplicateRun(request.RequestID)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "operator note")
		writeValidRun(request.RequestID, true)
	}
	os.Exit(0)
}

func fixtureCapabilities() Capabilities {
	return Capabilities{
		Protocols: []string{Protocol}, Provider: Provider{"fixture-adapter", "1.0.0"}, Languages: []string{"fixture"},
		Operations: []string{"index"}, RefreshModes: []string{"full"}, FactEncoding: FactEncoding,
		PositionEncoding: []string{"utf8-byte"}, Claims: Claims{Inputs: Inputs{Extensions: []string{".fixture"}}, Evidence: []string{"exact"}},
	}
}

func TestNormalizeCapabilitiesRejectsIncompleteAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Capabilities)
	}{
		{name: "missing full refresh", mutate: func(value *Capabilities) { value.RefreshModes = []string{"changed-units"} }},
		{name: "missing position encoding", mutate: func(value *Capabilities) { value.PositionEncoding = []string{"utf16"} }},
		{name: "empty evidence", mutate: func(value *Capabilities) { value.Claims.Evidence = nil }},
		{name: "marker only", mutate: func(value *Capabilities) { value.Claims.Inputs = Inputs{ProjectMarkers: []string{"fixture.project"}} }},
		{name: "reserved provider whitespace", mutate: func(value *Capabilities) { value.Provider.Name = "fixture adapter" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := fixtureCapabilities()
			test.mutate(&value)
			if _, err := NormalizeCapabilities(value); err == nil {
				t.Fatalf("capabilities were accepted: %#v", value)
			}
		})
	}
}

func TestClaimsDigestCanonicalizesOrder(t *testing.T) {
	left := Claims{Inputs: Inputs{Extensions: []string{".z", ".a"}, Filenames: []string{"z.file", "a.file"}}, Evidence: []string{"syntactic", "exact"}}
	right := Claims{Inputs: Inputs{Extensions: []string{".a", ".z"}, Filenames: []string{"a.file", "z.file"}}, Evidence: []string{"exact", "syntactic"}}
	leftDigest, leftErr := ClaimsDigest(left)
	rightDigest, rightErr := ClaimsDigest(right)
	if leftErr != nil || rightErr != nil || leftDigest != rightDigest {
		t.Fatalf("claim digests = %q/%v and %q/%v", leftDigest, leftErr, rightDigest, rightErr)
	}
}

func TestNormalizedCapabilitiesKeepRequiredEmptyArrays(t *testing.T) {
	value, err := NormalizeCapabilities(fixtureCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"executables":null`) {
		t.Fatalf("normalized capabilities used null executable list: %s", encoded)
	}
}

func TestFallbackResultCannotEscapeRoutedInputPaths(t *testing.T) {
	capabilities := fixtureCapabilities()
	capabilities.Claims.Fallback = true
	result := Result{Units: []graph.UnitFacts{{Documents: []graph.Document{{Path: "src/main.fixture"}}}}}
	if err := validateFallbackScope(result, capabilities, []string{"src/main.fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := validateFallbackScope(result, capabilities, []string{"other.fixture"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("fallback scope error = %v", err)
	}
	if err := validateFallbackScope(result, capabilities, nil); err != nil {
		t.Fatalf("manual fallback invocation was scoped: %v", err)
	}
}

func writeValidRun(requestID string, diagnostic bool) {
	provider := fixtureCapabilities().Provider
	unit := graph.Unit{ID: "fixture:unit", Provider: provider.Name, ProviderVersion: provider.Version, Language: "fixture"}
	document := graph.Document{ID: "fixture:doc", UnitID: unit.ID, Path: filepath.ToSlash("src/main.fixture"), Language: "fixture", Provider: provider.Name, ProviderVersion: provider.Version}
	symbol := graph.Symbol{ID: "fixture:symbol", UnitID: unit.ID, StableName: "fixture symbol", DisplayName: "Symbol", Kind: "function", DocumentID: document.ID, Provider: provider.Name, Evidence: graph.EvidenceExact}
	writeFrame(Protocol, requestID, "run.begin", runBegin{provider, FactEncoding})
	writeFrame(Protocol, requestID, "unit.begin", unitBegin{unit})
	writeFrame(Protocol, requestID, "facts", factBatch{Documents: []graph.Document{document}})
	writeFrame(Protocol, requestID, "facts", factBatch{Symbols: []graph.Symbol{symbol}})
	if diagnostic {
		writeFrame(Protocol, requestID, "diagnostic", Diagnostic{Severity: "info", Message: "indexed fixture", UnitID: unit.ID})
	}
	writeFrame(Protocol, requestID, "unit.end", unitEnd{Status: "complete", Counts: counts{Documents: 1, Symbols: 1}})
	writeFrame(Protocol, requestID, "run.end", runEnd{Status: "complete", Units: []string{unit.ID}})
}

func writeDuplicateRun(requestID string) {
	provider := fixtureCapabilities().Provider
	unit := graph.Unit{ID: "fixture:unit", Provider: provider.Name, ProviderVersion: provider.Version}
	document := graph.Document{ID: "fixture:doc", UnitID: unit.ID, Path: "one", Language: "fixture", Provider: provider.Name, ProviderVersion: provider.Version}
	writeFrame(Protocol, requestID, "run.begin", runBegin{provider, FactEncoding})
	writeFrame(Protocol, requestID, "unit.begin", unitBegin{unit})
	writeFrame(Protocol, requestID, "facts", factBatch{Documents: []graph.Document{document}})
	writeFrame(Protocol, requestID, "facts", factBatch{Documents: []graph.Document{document}})
	writeFrame(Protocol, requestID, "unit.end", unitEnd{Status: "complete", Counts: counts{Documents: 2}})
	writeFrame(Protocol, requestID, "run.end", runEnd{Status: "complete", Units: []string{unit.ID}})
}

func writeFrame(protocol, requestID, kind string, payload any) {
	encoded, _ := json.Marshal(payload)
	_ = json.NewEncoder(os.Stdout).Encode(frame{Protocol: protocol, RequestID: requestID, Kind: kind, Payload: encoded})
}
