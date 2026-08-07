package nativeindex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/repository"
)

func TestProviderRunsFullAdapterOnlyWhenDotnetSemanticInputsChange(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mixed")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "Mixed.csproj", "<Project />")
	writeFile(t, root, "Program.cs", "class Program {}")
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "weave@example.test")
	git(t, root, "config", "user.name", "Weave")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "initial")
	repo, err := repository.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "calls")
	provider := Provider{Path: os.Args[0], Args: []string{"-test.run=TestNativeAdapterHelperProcess", "--", marker}, Directory: root}
	refresh := func(previous *freshness.Manifest) freshness.Result {
		state, err := repo.Inspect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err := provider.Refresh(context.Background(), freshness.Request{Repository: repo, State: state, Previous: previous})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := refresh(nil)
	if len(first.Batches) != 1 || callCount(t, marker) != 2 {
		t.Fatalf("first refresh = %#v, calls %d", first, callCount(t, marker))
	}
	manifest := &freshness.Manifest{Units: first.Units}
	writeFile(t, root, "README.md", "docs only")
	second := refresh(manifest)
	if len(second.Batches) != 0 || callCount(t, marker) != 2 {
		t.Fatalf("docs refresh invoked adapter: %#v, calls %d", second, callCount(t, marker))
	}
	writeFile(t, root, "Program.cs", "class Program { int Value; }")
	third := refresh(manifest)
	if len(third.Batches) != 1 || callCount(t, marker) != 4 {
		t.Fatalf("semantic refresh = %#v, calls %d", third, callCount(t, marker))
	}
}

func TestNativeAdapterHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(os.Args) <= separator+2 {
		return
	}
	marker, operation := os.Args[separator+1], os.Args[separator+2]
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(file, operation)
	_ = file.Close()
	capabilities := map[string]any{
		"protocols": []string{adapter.Protocol}, "provider": map[string]string{"name": "fixture-dotnet", "version": "1"},
		"languages": []string{"csharp"}, "operations": []string{"index"}, "refresh_modes": []string{"full"},
		"fact_encoding": adapter.FactEncoding, "position_encodings": []string{"utf8-byte"},
		"requires": map[string]any{"executables": []string{}, "may_run_build_tool": true},
	}
	if operation == "describe" {
		_ = json.NewEncoder(os.Stdout).Encode(capabilities)
		os.Exit(0)
	}
	var request adapter.IndexRequest
	if err := json.NewDecoder(bufio.NewReader(os.Stdin)).Decode(&request); err != nil {
		t.Fatal(err)
	}
	frame := func(kind string, payload any) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol": adapter.Protocol, "request_id": request.RequestID, "kind": kind, "payload": payload})
	}
	frame("run.begin", map[string]any{"provider": capabilities["provider"], "fact_encoding": adapter.FactEncoding})
	unit := map[string]any{"id": "fixture:dotnet", "provider": "fixture-dotnet", "provider_version": "1", "language": "csharp"}
	frame("unit.begin", map[string]any{"unit": unit})
	frame("unit.end", map[string]any{"status": "complete", "counts": map[string]int{"documents": 0, "symbols": 0, "occurrences": 0, "edges": 0}})
	frame("run.end", map[string]any{"status": "complete", "units": []string{"fixture:dotnet"}})
	os.Exit(0)
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func callCount(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(content)))
}
