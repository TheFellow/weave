package goindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
)

func TestGoEnvironmentPreservesToolchainAndDisablesDependencyDownloads(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "go1.25.0+auto")
	t.Setenv("GOPROXY", "https://proxy.example.invalid")
	t.Setenv("GONOSUMDB", "example.invalid")
	environment := goEnvironment(false)
	values := map[string]string{}
	counts := map[string]int{}
	for _, value := range environment {
		name, setting, _ := strings.Cut(value, "=")
		values[name] = setting
		counts[name]++
	}
	if values["GOTOOLCHAIN"] != "go1.25.0+auto" || values["GOPROXY"] != "off" || values["GONOSUMDB"] != "*" {
		t.Fatalf("environment = %#v", values)
	}
	if counts["GOPROXY"] != 1 || counts["GONOSUMDB"] != 1 {
		t.Fatalf("duplicate policy settings: %#v", counts)
	}
}

func TestProviderEmitsCompilerResolvedGoFacts(t *testing.T) {
	root := fixtureModule(t)
	provider := Provider{}
	result, err := provider.Refresh(context.Background(), request(root, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batches) != 3 || len(result.Units) != 3 {
		t.Fatalf("packages: %d batches, %d units", len(result.Batches), len(result.Units))
	}

	var symbols []graph.Symbol
	var edges []graph.Edge
	var occurrences []graph.Occurrence
	for _, batch := range result.Batches {
		if err := batch.Validate(); err != nil {
			t.Fatalf("invalid batch %s: %v", batch.Unit.ID, err)
		}
		if batch.Unit.InputFingerprint == "" || batch.Unit.SurfaceFingerprint == "" || batch.Unit.InventoryDigest == "" {
			t.Fatalf("missing fingerprints: %#v", batch.Unit)
		}
		symbols = append(symbols, batch.Symbols...)
		edges = append(edges, batch.Edges...)
		occurrences = append(occurrences, batch.Occurrences...)
	}
	expected := []string{"Handler", "Handle", "Box", "Service", "Run", "TestService"}
	if runtime.GOOS == "windows" {
		expected = append(expected, "WindowsOnly")
	} else {
		expected = append(expected, "Platform")
	}
	for _, name := range expected {
		if !hasSymbol(symbols, name) {
			t.Errorf("missing symbol %q", name)
		}
	}
	if runtime.GOOS != "windows" && hasSymbol(symbols, "WindowsOnly") {
		t.Error("indexed a file excluded by the active build constraints")
	}
	if !hasEdge(edges, graph.EdgeImplements, "Service", "Handler", symbols) {
		t.Error("missing compiler-resolved Service implements Handler edge")
	}
	if countKind(edges, graph.EdgeImplements) < 2 {
		t.Error("missing concrete-method to interface-method implementation edge")
	}
	if !hasEdge(edges, graph.EdgeCalls, "Run", "Invoke", symbols) {
		t.Error("missing static Run -> Invoke call")
	}
	if !hasEdge(edges, graph.EdgeCalls, "Invoke", "Handle", symbols) {
		t.Error("missing interface Invoke -> Handler.Handle call")
	}
	if countKind(edges, graph.EdgeImports) < 2 || countKind(edges, graph.EdgeDependsOn) < 2 {
		t.Fatalf("missing import/dependency facts: %#v", edges)
	}
	if len(occurrences) == 0 {
		t.Fatal("no typed occurrences")
	}
	for _, occurrence := range occurrences {
		if occurrence.Range.End.Byte < occurrence.Range.Start.Byte || occurrence.Range.Start.Line < 0 {
			t.Fatalf("invalid exact source evidence: %#v", occurrence)
		}
	}

	// A forced rebuild of identical inputs must normalize byte-for-byte.
	again, err := provider.Refresh(context.Background(), request(root, nil))
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(result.Batches)
	right, _ := json.Marshal(again.Batches)
	if string(left) != string(right) {
		at := 0
		for at < len(left) && at < len(right) && left[at] == right[at] {
			at++
		}
		start := max(0, at-300)
		endLeft, endRight := min(at+300, len(left)), min(at+300, len(right))
		t.Fatalf("identical source produced a nondeterministic normalized graph at %d:\n%s\n%s", at, left[start:endLeft], right[start:endRight])
	}
}

func TestProviderReplacesOnlyFingerprintAffectedPackages(t *testing.T) {
	root := fixtureModule(t)
	provider := Provider{}
	first, err := provider.Refresh(context.Background(), request(root, nil))
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestFrom(provider, first)

	write(t, root, "impl/impl.go", `package impl

import "example.test/weavefixture/api"

type Service struct{}
func (Service) Handle(value string) string { return value + "!" }
func Run() string { return api.Invoke(Service{}) }
`)
	implementation, err := provider.Refresh(context.Background(), request(root, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if got := batchPaths(implementation.Batches); got != "example.test/weavefixture/impl" {
		t.Fatalf("implementation-only batch paths = %q", got)
	}

	manifest = manifestFrom(provider, implementation)
	write(t, root, "api/api.go", `package api

type Handler interface { Handle(string) string }
type Box[T any] struct { Value T }
func Invoke(handler Handler) string { return handler.Handle("x") }
func Version() int { return 1 }
`)
	publicChange, err := provider.Refresh(context.Background(), request(root, manifest))
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Split(batchPaths(publicChange.Batches), ",")
	if !slices.Contains(paths, "example.test/weavefixture/api") || !slices.Contains(paths, "example.test/weavefixture/impl") {
		t.Fatalf("public API change did not reach reverse dependency: %q", paths)
	}
}

func TestProviderRejectsIllTypedRefresh(t *testing.T) {
	root := fixtureModule(t)
	write(t, root, "api/api.go", "package api\nfunc Broken( {\n")
	_, err := (Provider{}).Refresh(context.Background(), request(root, nil))
	if err == nil || !strings.Contains(err.Error(), "Go package load failed") {
		t.Fatalf("malformed package error = %v", err)
	}
}

func TestProviderRejectsTargetNewerThanBinaryActionably(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/future\n\ngo 1.99\n")
	write(t, root, "future.go", "package future\n")
	_, err := (Provider{}).Refresh(context.Background(), request(root, nil))
	if err == nil || !strings.Contains(err.Error(), "requires Go 1.99") ||
		!strings.Contains(err.Error(), "install or build Weave") ||
		!strings.Contains(err.Error(), "does not download toolchains") {
		t.Fatalf("future toolchain error = %v", err)
	}
}

func TestProviderLeavesNonGoRepositoryEmpty(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "hello\n")
	previous := &freshness.Manifest{Units: []freshness.Unit{{ID: "old"}}}
	result, err := (Provider{}).Refresh(context.Background(), request(root, previous))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Units) != 0 || strings.Join(result.Removed, ",") != "old" {
		t.Fatalf("empty result = %#v", result)
	}
}

func fixtureModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/weavefixture\n\ngo 1.24.0\n")
	write(t, root, "api/api.go", `package api

type Handler interface { Handle(string) string }
type Box[T any] struct { Value T }
func Invoke(handler Handler) string { return handler.Handle("x") }
`)
	write(t, root, "impl/impl.go", `package impl

import "example.test/weavefixture/api"

type Service struct{}
func (Service) Handle(value string) string { return value }
func Run() string { return api.Invoke(Service{}) }
`)
	write(t, root, "impl/impl_test.go", `package impl

import "testing"
func TestService(t *testing.T) { if Run() == "" { t.Fatal("empty") } }
`)
	write(t, root, "cmd/tool/main.go", `package main

import (
    "fmt"
    "example.test/weavefixture/impl"
)
func main() { fmt.Println(impl.Run()) }
`)
	write(t, root, "api/platform_default.go", `//go:build !windows

package api
const Platform = "default"
`)
	write(t, root, "api/platform_windows.go", `//go:build windows

package api
const WindowsOnly = true
`)
	return root
}

func request(root string, previous *freshness.Manifest) freshness.Request {
	return freshness.Request{Repository: repository.Repository{Root: root, Identity: "example.test/repository"}, Previous: previous}
}

func manifestFrom(provider Provider, result freshness.Result) *freshness.Manifest {
	return &freshness.Manifest{Provider: provider.ID(), Units: result.Units}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasSymbol(symbols []graph.Symbol, name string) bool {
	for _, symbol := range symbols {
		if symbol.DisplayName == name {
			return true
		}
	}
	return false
}

func symbolName(symbols []graph.Symbol, id string) string {
	for _, symbol := range symbols {
		if symbol.ID == id {
			return symbol.DisplayName
		}
	}
	return ""
}

func hasEdge(edges []graph.Edge, kind graph.EdgeKind, from, to string, symbols []graph.Symbol) bool {
	for _, edge := range edges {
		if edge.Kind == kind && symbolName(symbols, edge.From) == from && symbolName(symbols, edge.To) == to {
			return true
		}
	}
	return false
}

func countKind(edges []graph.Edge, kind graph.EdgeKind) int {
	count := 0
	for _, edge := range edges {
		if edge.Kind == kind {
			count++
		}
	}
	return count
}

func batchPaths(batches []graph.UnitFacts) string {
	var paths []string
	for _, batch := range batches {
		for _, symbol := range batch.Symbols {
			if symbol.Kind == "package" {
				paths = append(paths, symbol.DisplayName)
				break
			}
		}
	}
	slices.Sort(paths)
	return strings.Join(paths, ",")
}
