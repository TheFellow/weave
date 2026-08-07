package application

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/architecture"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
)

func TestAdapterDoctorNegotiatesNativeProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	path := filepath.Join(t.TempDir(), "weave-dotnet")
	program := `#!/bin/sh
printf '%s\n' '{"protocols":["weave.adapter/v0"],"provider":{"name":"weave-dotnet","version":"1.2.3"},"languages":["fsharp","csharp"],"operations":["index"],"refresh_modes":["full"],"fact_encoding":"weave.facts/v0","position_encodings":["utf8-byte"],"requires":{"executables":["dotnet"],"may_run_build_tool":true}}'
`
	if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEAVE_DOTNET_ADAPTER", path)
	t.Setenv("WEAVE_PYTHON_ADAPTER", "")
	t.Setenv("WEAVE_SCIP_DOTNET", "")
	statuses := inspectAdapters(context.Background(), true, adapter.Runner{})
	if len(statuses) < 1 {
		t.Fatal("doctor returned no statuses")
	}
	status := statuses[0]
	if !status.Available || !status.Checked || !status.Compatible || status.Provider != "weave-dotnet" || status.Version != "1.2.3" || strings.Join(status.Languages, ",") != "csharp,fsharp" {
		t.Fatalf("status = %#v", status)
	}
}

func TestConfiguredAdapterPathMustExistAndBeExecutable(t *testing.T) {
	t.Setenv("WEAVE_DOTNET_ADAPTER", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("WEAVE_PYTHON_ADAPTER", "")
	t.Setenv("WEAVE_SCIP_DOTNET", "")
	statuses := inspectAdapters(context.Background(), false, adapter.Runner{})
	if statuses[0].Available || !strings.Contains(statuses[0].Detail, "configured path unavailable") {
		t.Fatalf("status = %#v", statuses[0])
	}
}

func TestRegisteredAdapterDoctorPreservesLiteralArgumentsAndProviderIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	path := filepath.Join(t.TempDir(), "custom-adapter")
	program := `#!/bin/sh
[ "$1" = 'space value;$(literal)' ] || exit 9
shift
[ "$1" = describe ] || exit 10
printf '%s\n' '{"protocols":["weave.adapter/v0"],"provider":{"name":"custom-adapter","version":"1.0.0"},"languages":["custom"],"operations":["index"],"refresh_modes":["full"],"fact_encoding":"weave.facts/v0","position_encodings":["utf8-byte"],"requires":{"executables":[],"may_run_build_tool":false}}'
`
	if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	registration := adapter.Registration{
		Name: "custom-adapter", Command: []string{path, "space value;$(literal)"},
		Inputs: adapter.Inputs{Extensions: []string{".custom"}},
	}
	statuses := inspectAdaptersWithError(context.Background(), true, adapter.Runner{}, nil, registration)
	for _, status := range statuses {
		if status.Name != registration.Name {
			continue
		}
		if !status.Available || !status.Checked || !status.Compatible || status.Provider != registration.Name {
			t.Fatalf("registered adapter status = %#v", status)
		}
		return
	}
	t.Fatal("registered adapter status is absent")
}

func TestCppDoctorSeparatesExecutableAndFactProviderNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	path := filepath.Join(t.TempDir(), "weave-cpp")
	program := `#!/bin/sh
printf '%s\n' '{"protocols":["weave.adapter/v0"],"provider":{"name":"scip:scip-clang","version":"0.4.0"},"languages":["c","cpp","cuda"],"operations":["index"],"refresh_modes":["full"],"fact_encoding":"weave.facts/v0","position_encodings":["utf8-byte"],"requires":{"executables":["scip-clang"],"may_run_build_tool":true}}'
`
	if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEAVE_CPP_ADAPTER", path)
	statuses := inspectAdapters(context.Background(), true, adapter.Runner{})
	for _, status := range statuses {
		if status.Name != "weave-cpp" {
			continue
		}
		if !status.Available || !status.Checked || !status.Compatible || status.Provider != "scip:scip-clang" || status.Version != "0.4.0" {
			t.Fatalf("C++ adapter status = %#v", status)
		}
		return
	}
	t.Fatal("C++ adapter status is absent")
}

func TestTypeScriptAndJVMDoctorUseSCIPProviderIdentities(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX executable scripts")
	}
	tests := []struct {
		name, environment, executable, provider, version, languages string
	}{
		{"TypeScript", "WEAVE_TYPESCRIPT_ADAPTER", "weave-typescript", "scip:scip-typescript", "0.4.0", `"javascript","typescript"`},
		{"JVM", "WEAVE_JVM_ADAPTER", "weave-jvm", "scip:scip-java", "0.13.1", `"java","kotlin"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.executable)
			program := `#!/bin/sh
printf '%s\n' '{"protocols":["weave.adapter/v0"],"provider":{"name":"` + test.provider + `","version":"` + test.version + `"},"languages":[` + test.languages + `],"operations":["index"],"refresh_modes":["full"],"fact_encoding":"weave.facts/v0","position_encodings":["utf8-byte"],"requires":{"executables":[],"may_run_build_tool":true}}'
`
			if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv(test.environment, path)
			statuses := inspectAdapters(context.Background(), true, adapter.Runner{})
			for _, status := range statuses {
				if status.Name != test.executable {
					continue
				}
				if !status.Available || !status.Checked || !status.Compatible || status.Provider != test.provider || status.Version != test.version {
					t.Fatalf("%s adapter status = %#v", test.name, status)
				}
				return
			}
			t.Fatalf("%s adapter status is absent", test.name)
		})
	}
}

func TestCIFatalIssueClassification(t *testing.T) {
	warnings := []storage.Issue{{Severity: storage.IssueWarning, Kind: "unresolved-occurrence", Record: "external"}}
	if hasFatalIssues(warnings) {
		t.Fatal("open-world unresolved occurrence was fatal")
	}
	errors := append(warnings, storage.Issue{Severity: storage.IssueError, Kind: "orphan-document", Record: "broken"})
	if !hasFatalIssues(errors) {
		t.Fatal("ownership damage was not fatal")
	}
}

func TestIntegrityIssuesAreVisibleInSARIF(t *testing.T) {
	config := architecture.Config{Schema: architecture.Schema, Rules: []architecture.Rule{{ID: "boundary", Action: "forbid"}}}
	report := architecture.Report{Schema: architecture.ReportSchema, Violations: []architecture.Violation{{
		RuleID: "boundary", Message: "boundary crossed", Document: "main.go",
		Range: graph.Range{Start: graph.Position{Line: 2, Column: 3}, End: graph.Position{Line: 2, Column: 4}},
	}}}
	log := architecture.SARIF(config, report)
	attachIntegritySARIF(&log, []storage.Issue{
		{Severity: storage.IssueWarning, Kind: "unresolved-occurrence", Record: "external", Detail: "not materialized"},
		{Severity: storage.IssueError, Kind: "orphan-document", Record: "broken", Detail: "unit absent"},
	})
	if len(log.Runs) != 1 || len(log.Runs[0].Results) != 1 || len(log.Runs[0].Invocations) != 1 {
		t.Fatalf("SARIF = %#v", log)
	}
	for _, result := range log.Runs[0].Results {
		if len(result.Locations) == 0 {
			t.Fatalf("source result has no location: %#v", result)
		}
	}
	notifications := log.Runs[0].Invocations[0].ToolExecutionNotifications
	if len(notifications) != 2 || notifications[0].Level != "warning" || notifications[1].Level != "error" || log.Runs[0].Invocations[0].ExecutionSuccessful {
		t.Fatalf("notifications = %#v", notifications)
	}
}
