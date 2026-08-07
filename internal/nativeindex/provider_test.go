package nativeindex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

func TestProviderUsesPythonInputProfileAndRuntimeVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "python")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "example.py", "value = 1")
	writeFile(t, root, "Example.cs", "class Example {}")
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
	provider := Provider{
		Name: "fixture-dotnet", Path: os.Args[0],
		Args:      []string{"-test.run=TestNativeAdapterHelperProcess", "--", marker},
		Directory: root, Profile: PythonInputs, ProbeProviderVersion: true,
	}
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
	if len(first.Batches) != 1 || callCount(t, marker) != 3 {
		t.Fatalf("first refresh = %#v, calls %d", first, callCount(t, marker))
	}
	manifest := &freshness.Manifest{Units: first.Units}
	writeFile(t, root, "Example.cs", "class Example { int Value; }")
	second := refresh(manifest)
	if len(second.Batches) != 0 || callCount(t, marker) != 4 {
		t.Fatalf("non-Python refresh invoked index: %#v, calls %d", second, callCount(t, marker))
	}
	writeFile(t, root, "example.py", "value = 2")
	third := refresh(manifest)
	if len(third.Batches) != 1 || callCount(t, marker) != 7 {
		t.Fatalf("Python refresh = %#v, calls %d", third, callCount(t, marker))
	}
}

func TestProviderUsesConfiguredInputsForArbitraryAdapter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "arbitrary")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "main.widget", "first")
	writeFile(t, root, "ignored.txt", "first")
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
	provider := Provider{
		Name: "fixture-dotnet", Path: os.Args[0], Args: []string{"-test.run=TestNativeAdapterHelperProcess", "--", marker},
		Directory: root, Profile: InputProfile("widget"), Inputs: adapter.Inputs{Extensions: []string{".widget"}},
		ProbeProviderVersion: true, ConfigFingerprint: "fixture-config",
	}
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
	if len(first.Batches) != 1 || callCount(t, marker) != 3 {
		t.Fatalf("first refresh = %#v, calls %d", first, callCount(t, marker))
	}
	manifest := &freshness.Manifest{Units: first.Units}
	writeFile(t, root, "ignored.txt", "second")
	second := refresh(manifest)
	if len(second.Batches) != 0 || callCount(t, marker) != 4 {
		t.Fatalf("ignored refresh = %#v, calls %d", second, callCount(t, marker))
	}
	writeFile(t, root, "main.widget", "second")
	third := refresh(manifest)
	if len(third.Batches) != 1 || callCount(t, marker) != 7 {
		t.Fatalf("selected refresh = %#v, calls %d", third, callCount(t, marker))
	}
}

func TestDefaultFailsClosedWhenRegisteredCommandIsUnavailable(t *testing.T) {
	registration := adapter.Registration{
		Name: "missing-fixture", Command: []string{"weave-adapter-that-does-not-exist"},
		Inputs: adapter.Inputs{Extensions: []string{".fixture"}}, ConfigFingerprint: "fixture-config",
	}
	composite, ok := Default(t.TempDir(), registration).(freshness.CompositeProvider)
	if !ok {
		t.Fatalf("Default() = %T", Default(t.TempDir(), registration))
	}
	for _, child := range composite.Providers {
		if child.ID().Name != "native/missing-fixture" {
			continue
		}
		if _, err := child.Refresh(context.Background(), freshness.Request{}); err == nil || !strings.Contains(err.Error(), "find registered adapter") {
			t.Fatalf("missing registered adapter error = %v", err)
		}
		return
	}
	t.Fatal("missing registered adapter was silently omitted")
}

func TestDefaultCanonicalizesRegisteredProviderOrder(t *testing.T) {
	registrations := []adapter.Registration{
		{Name: "z-provider", Command: []string{os.Args[0]}, Inputs: adapter.Inputs{Extensions: []string{".z"}}, ConfigFingerprint: "z"},
		{Name: "a-provider", Command: []string{os.Args[0]}, Inputs: adapter.Inputs{Extensions: []string{".a"}}, ConfigFingerprint: "a"},
	}
	composite := Default(t.TempDir(), registrations...).(freshness.CompositeProvider)
	var owners []string
	for _, child := range composite.Providers {
		if child.ID().Name == "native/a-provider" || child.ID().Name == "native/z-provider" {
			owners = append(owners, child.ID().Name)
		}
	}
	if strings.Join(owners, ",") != "native/a-provider,native/z-provider" {
		t.Fatalf("registered provider order = %q", owners)
	}
}

func TestBuiltInRustAndCppAdaptersUseProjectActivationAndSemanticInputs(t *testing.T) {
	t.Setenv("WEAVE_RUST_ADAPTER", os.Args[0])
	t.Setenv("WEAVE_CPP_ADAPTER", os.Args[0])
	composite := Default(t.TempDir()).(freshness.CompositeProvider)
	providers := map[string]Provider{}
	for _, child := range composite.Providers {
		provider, ok := child.(Provider)
		if ok {
			providers[provider.Name] = provider
		}
	}
	rust, ok := providers["weave-rust"]
	if !ok || !rust.Permissions.BuildTool || !rust.ProbeProviderVersion {
		t.Fatalf("Rust provider = %#v, present %v", rust, ok)
	}
	if rust.active([]string{"src/lib.rs"}) || !rust.active([]string{"Cargo.toml", "src/lib.rs"}) {
		t.Fatalf("Rust activation accepted source-only repository or rejected Cargo project")
	}
	for _, path := range []string{"src/lib.rs", "Cargo.lock", ".cargo/config", ".cargo/config.toml", "rust-toolchain.toml", "templates/embedded.txt"} {
		if !isSemanticInputFor(path, RustInputs, rust.Inputs) {
			t.Fatalf("Rust semantic input %q was not selected", path)
		}
	}

	cpp, ok := providers["scip:scip-clang"]
	if !ok || cpp.Path != os.Args[0] || !cpp.Permissions.BuildTool || !cpp.ProbeProviderVersion {
		t.Fatalf("C++ provider = %#v, present %v", cpp, ok)
	}
	if cpp.active([]string{"src/main.cpp"}) || !cpp.active([]string{"compile_commands.json", "src/main.cpp"}) {
		t.Fatalf("C++ activation accepted source-only repository or rejected compilation database")
	}
	for _, path := range []string{"src/main.c", "src/main.cpp", "include/api.hpp", "include/generated.inc", "compile_commands.json", ".clangd", "build-flags.txt"} {
		if !isSemanticInputFor(path, CppInputs, cpp.Inputs) {
			t.Fatalf("C++ semantic input %q was not selected", path)
		}
	}
}

func TestBuiltInTypeScriptAdapterUsesRootProjectAndConservativeInputs(t *testing.T) {
	t.Setenv("WEAVE_TYPESCRIPT_ADAPTER", os.Args[0])
	composite := Default(t.TempDir()).(freshness.CompositeProvider)
	var typescript Provider
	for _, child := range composite.Providers {
		provider, ok := child.(Provider)
		if ok && provider.Name == "scip:scip-typescript" {
			typescript = provider
			break
		}
	}
	if typescript.Path != os.Args[0] || typescript.Permissions != (adapter.Permissions{}) || !typescript.ProbeProviderVersion {
		t.Fatalf("TypeScript provider = %#v", typescript)
	}
	if typescript.active([]string{"src/main.ts", "packages/web/tsconfig.json"}) ||
		!typescript.active([]string{"tsconfig.json", "src/main.ts"}) ||
		!typescript.active([]string{"jsconfig.json", "src/main.js"}) {
		t.Fatal("TypeScript automatic activation did not require a root project configuration")
	}
	for _, path := range []string{"src/main.ts", "src/view.tsx", "package.json", "pnpm-lock.yaml", "assets/schema.graphql"} {
		if !isSemanticInputFor(path, TypeScriptInputs, typescript.Inputs) {
			t.Fatalf("TypeScript semantic input %q was not conservatively selected", path)
		}
	}
}

func TestTypeScriptAutomaticActivationDoesNotGuessNestedProject(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "packages", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "packages/web/tsconfig.json", `{}`)
	writeFile(t, root, "packages/web/main.ts", "export const value = 1")
	activation := adapter.Inputs{Filenames: []string{"tsconfig.json", "jsconfig.json"}}
	paths, _, err := semanticInputsForActivation(context.Background(), root, TypeScriptInputs, adapter.Inputs{}, activation)
	if err != nil || len(paths) != 0 {
		t.Fatalf("nested-only TypeScript project activated automatically: %q, %v", paths, err)
	}
	writeFile(t, root, "tsconfig.json", `{}`)
	paths, _, err = semanticInputsForActivation(context.Background(), root, TypeScriptInputs, adapter.Inputs{}, activation)
	if err != nil || !slices.Contains(paths, "packages/web/main.ts") || !slices.Contains(paths, "tsconfig.json") {
		t.Fatalf("root TypeScript project inputs = %q, %v", paths, err)
	}
}

func TestProviderFingerprintIncludesAdapterIdentity(t *testing.T) {
	inputs := "sha256:inputs"
	first := providerFingerprint(freshness.ProviderID{Name: "native/weave-dotnet", Version: "1.binary-a"}, inputs)
	second := providerFingerprint(freshness.ProviderID{Name: "native/weave-dotnet", Version: "1.binary-b"}, inputs)
	if first == second || first != providerFingerprint(freshness.ProviderID{Name: "native/weave-dotnet", Version: "1.binary-a"}, inputs) {
		t.Fatalf("provider fingerprints are not deterministic or upgrade-sensitive: %q %q", first, second)
	}
}

func TestCappedBufferBoundsRetainedInventory(t *testing.T) {
	buffer := cappedBuffer{limit: 3}
	if count, err := buffer.Write([]byte("abcdef")); err != nil || count != 6 {
		t.Fatalf("Write() = %d, %v", count, err)
	}
	if !buffer.exceeded || buffer.String() != "abc" {
		t.Fatalf("buffer = %q, exceeded %v", buffer.String(), buffer.exceeded)
	}
}

func TestSemanticInputsDisableRepositoryFSMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fsmonitor fixture uses a POSIX script")
	}
	root := filepath.Join(t.TempDir(), "python")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	writeFile(t, root, "example.py", "value = 1")
	marker := filepath.Join(root, ".git", "fsmonitor-ran")
	hook := filepath.Join(root, ".git", "fsmonitor-test.sh")
	writeFile(t, root, ".git/fsmonitor-test.sh", fmt.Sprintf("#!/bin/sh\nprintf ran > %q\n", marker))
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "core.fsmonitor", hook)
	if _, _, err := semanticInputs(context.Background(), root, PythonInputs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository fsmonitor executed: %v", err)
	}
}

func TestPythonSemanticInputsRejectSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	writeFile(t, root, "target.txt", "value = 1")
	if err := os.Symlink("target.txt", filepath.Join(root, "link.py")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := semanticInputs(context.Background(), root, PythonInputs); err == nil || !strings.Contains(err.Error(), "source is a symlink") {
		t.Fatalf("semanticInputs() error = %v, want symlink rejection", err)
	}
}

func TestConfiguredSemanticInputsRejectSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	writeFile(t, root, "target.txt", "value = 1")
	if err := os.Symlink("target.txt", filepath.Join(root, "link.widget")); err != nil {
		t.Fatal(err)
	}
	inputs := adapter.Inputs{Extensions: []string{".widget"}}
	if _, _, err := semanticInputsFor(context.Background(), root, InputProfile("widget"), inputs); err == nil || !strings.Contains(err.Error(), "source is a symlink") {
		t.Fatalf("semanticInputsFor() error = %v, want symlink rejection", err)
	}
}

func TestCompilerProviderActivationPrecedesConservativeFileReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	writeFile(t, root, "target.txt", "pub fn value() {}")
	if err := os.Symlink("target.txt", filepath.Join(root, "lib.rs")); err != nil {
		t.Fatal(err)
	}
	activation := adapter.Inputs{Filenames: []string{"cargo.toml", "rust-project.json"}}
	paths, _, err := semanticInputsForActivation(context.Background(), root, RustInputs, adapter.Inputs{}, activation)
	if err != nil || len(paths) != 0 {
		t.Fatalf("inactive Rust inputs = %q, %v", paths, err)
	}
	writeFile(t, root, "Cargo.toml", "[package]\nname='fixture'\nversion='0.0.0'\n")
	if _, _, err := semanticInputsForActivation(context.Background(), root, RustInputs, adapter.Inputs{}, activation); err == nil || !strings.Contains(err.Error(), "source is a symlink") {
		t.Fatalf("active Rust symlink error = %v", err)
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
