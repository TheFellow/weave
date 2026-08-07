package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		PositionEncoding: []string{"utf8-byte"},
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
