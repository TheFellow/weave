package adapter

import (
	"slices"
	"strings"
	"testing"
)

func TestChildEnvironmentIsBoundedAndSharedAcrossToolchains(t *testing.T) {
	t.Setenv("DOTNET_ROOT", "/toolchains/dotnet")
	t.Setenv("WEAVE_SCIP_TYPESCRIPT", "/toolchains/scip-typescript")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "not-for-adapters")
	environment := ChildEnvironment()
	if !slices.Contains(environment, "DOTNET_ROOT=/toolchains/dotnet") ||
		!slices.Contains(environment, "WEAVE_SCIP_TYPESCRIPT=/toolchains/scip-typescript") {
		t.Fatalf("toolchain environment = %#v", environment)
	}
	if slices.ContainsFunc(environment, func(value string) bool { return strings.HasPrefix(value, "AWS_SECRET_ACCESS_KEY=") }) {
		t.Fatalf("unbounded ambient environment = %#v", environment)
	}
}

func TestEnvironmentRegistrationsAreExplicitAndBounded(t *testing.T) {
	values := map[string]string{
		"WEAVE_PYTHON_ADAPTER":     "/tools/weave-python",
		"WEAVE_DOTNET_ADAPTER":     "/tools/weave-dotnet",
		"WEAVE_TYPESCRIPT_ADAPTER": "",
	}
	registrations, err := EnvironmentRegistrations(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 2 {
		t.Fatalf("registrations = %#v", registrations)
	}
	python := registrations[1]
	if python.Name != "weave-python" || python.Source != "environment" || !slices.Equal(python.Command, []string{"/tools/weave-python"}) {
		t.Fatalf("Python registration = %#v", python)
	}
	if !slices.Contains(python.Claims.Inputs.Extensions, ".py") || python.ConfigFingerprint == "" || python.CapabilityDigest != "" {
		t.Fatalf("Python compatibility authority = %#v", python)
	}
	dotnet := registrations[0]
	if len(dotnet.Claims.Inputs.ProjectMarkers) != 0 || !dotnet.Permissions.BuildTool {
		t.Fatalf(".NET activation = %#v", dotnet)
	}
}
