package adapter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadRegistryPreservesLiteralArgvAndCanonicalizesRegistrations(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "adapters.json")
	content := `{
  "schema": "weave.adapters/v1",
  "adapters": [
    {
      "name": "weave-zig",
      "command": ["weave-zig", "--define=message=$(touch nope)", "", "; echo nope"],
      "inputs": {"extensions": [".ZIG", ".zig"], "filenames": ["build.zig"]},
      "timeout": "90s"
    },
    {
      "name": "acme-rust",
      "command": ["./bin/weave-rust", "--variant", "all targets"],
      "inputs": {"extensions": [".rs"], "filenames": ["Cargo.toml", "cargo.toml"]},
      "permissions": {"build_tool": true}
    }
  ]
}`
	writeRegistry(t, path, content)
	registrations, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 2 || registrations[0].Name != "acme-rust" || registrations[1].Name != "weave-zig" {
		t.Fatalf("registrations are not canonical: %#v", registrations)
	}
	rust := registrations[0]
	wantCommand := []string{filepath.Join(directory, "bin", "weave-rust"), "--variant", "all targets"}
	if !reflect.DeepEqual(rust.Command, wantCommand) || !reflect.DeepEqual(rust.Inputs, Inputs{Extensions: []string{".rs"}, Filenames: []string{"cargo.toml"}}) {
		t.Fatalf("rust registration = %#v", rust)
	}
	if !rust.Permissions.BuildTool || rust.ConfigFingerprint == "" {
		t.Fatalf("rust trust/fingerprint = %#v", rust)
	}
	zig := registrations[1]
	if want := []string{"weave-zig", "--define=message=$(touch nope)", "", "; echo nope"}; !reflect.DeepEqual(zig.Command, want) {
		t.Fatalf("literal argv = %#v, want %#v", zig.Command, want)
	}
	if zig.Timeout != 90*time.Second || !reflect.DeepEqual(zig.Inputs.Extensions, []string{".zig"}) {
		t.Fatalf("zig registration = %#v", zig)
	}
}

func TestRegistryFingerprintIncludesSourcePathAndCanonicalEntry(t *testing.T) {
	content := `{"schema":"weave.adapters/v1","adapters":[{"name":"acme-rust","command":["acme-rust"],"inputs":{"extensions":[".rs"]}}]}`
	leftPath := filepath.Join(t.TempDir(), "adapters.json")
	rightPath := filepath.Join(t.TempDir(), "adapters.json")
	writeRegistry(t, leftPath, content)
	writeRegistry(t, rightPath, content)
	left, err := LoadRegistry(leftPath)
	if err != nil {
		t.Fatal(err)
	}
	right, err := LoadRegistry(rightPath)
	if err != nil {
		t.Fatal(err)
	}
	if left[0].ConfigFingerprint == right[0].ConfigFingerprint {
		t.Fatal("registry path did not participate in fingerprint")
	}
	writeRegistry(t, rightPath, strings.Replace(content, `"command":["acme-rust"]`, `"command":["acme-rust","--all"]`, 1))
	changed, err := LoadRegistry(rightPath)
	if err != nil {
		t.Fatal(err)
	}
	if changed[0].ConfigFingerprint == right[0].ConfigFingerprint {
		t.Fatal("registration did not participate in fingerprint")
	}
}

func TestRegistryFingerprintDoesNotInvalidateUnchangedPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adapters.json")
	first := `{"schema":"weave.adapters/v1","adapters":[{"name":"a","command":["a"],"inputs":{"extensions":[".a"]}},{"name":"b","command":["b"],"inputs":{"extensions":[".b"]}}]}`
	writeRegistry(t, path, first)
	before, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, path, strings.Replace(first, `"command":["b"]`, `"command":["b","--changed"]`, 1))
	after, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ConfigFingerprint != after[0].ConfigFingerprint || before[1].ConfigFingerprint == after[1].ConfigFingerprint {
		t.Fatalf("peer fingerprints before=%#v after=%#v", before, after)
	}
}

func TestLoadRegistryRejectsAmbiguousOrUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name, content, contains string
	}{
		{"unknown schema", `{"schema":"other","adapters":[]}`, "want \"weave.adapters/v1\""},
		{"unknown field", `{"schema":"weave.adapters/v1","extra":true,"adapters":[]}`, "unknown field"},
		{"reserved name", `{"schema":"weave.adapters/v1","adapters":[{"name":"weave-python","command":["other"],"inputs":{"extensions":[".py"]}}]}`, "reserved"},
		{"duplicate name", `{"schema":"weave.adapters/v1","adapters":[{"name":"same","command":["one"],"inputs":{"extensions":[".one"]}},{"name":"same","command":["two"],"inputs":{"extensions":[".two"]}}]}`, "duplicate"},
		{"shell command", `{"schema":"weave.adapters/v1","adapters":[{"name":"shell","command":[],"inputs":{"extensions":[".x"]}}]}`, "command must contain"},
		{"empty executable", `{"schema":"weave.adapters/v1","adapters":[{"name":"empty","command":[""],"inputs":{"extensions":[".x"]}}]}`, "command[0] is empty"},
		{"missing inputs", `{"schema":"weave.adapters/v1","adapters":[{"name":"all","command":["all"],"inputs":{}}]}`, "at least one"},
		{"path input", `{"schema":"weave.adapters/v1","adapters":[{"name":"bad","command":["bad"],"inputs":{"filenames":["src/file"]}}]}`, "base name"},
		{"trailing value", `{"schema":"weave.adapters/v1","adapters":[]} {}`, "multiple JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "adapters.json")
			writeRegistry(t, path, test.content)
			if _, err := LoadRegistry(path); err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("LoadRegistry() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestLoadRegistryEmptyPathHasNoImplicitDiscovery(t *testing.T) {
	registrations, err := LoadRegistry("")
	if err != nil || registrations != nil {
		t.Fatalf("LoadRegistry(empty) = %#v, %v", registrations, err)
	}
}

func TestJVMProviderCanOptIntoAutomaticRefreshThroughTrustedRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adapters.json")
	writeRegistry(t, path, `{
  "schema":"weave.adapters/v1",
  "adapters":[{
    "name":"scip:scip-java",
    "command":["weave-jvm"],
    "inputs":{"extensions":[".java",".kt"],"filenames":["pom.xml","build.gradle","build.gradle.kts"]},
    "permissions":{"network":true,"restore":true,"build_tool":true,"run_generators":true}
  }]
}`)
	registrations, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 1 || registrations[0].Name != "scip:scip-java" ||
		!registrations[0].Permissions.Network || !registrations[0].Permissions.Restore ||
		!registrations[0].Permissions.BuildTool || !registrations[0].Permissions.RunGenerators {
		t.Fatalf("JVM registration = %#v", registrations)
	}
}

func writeRegistry(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
