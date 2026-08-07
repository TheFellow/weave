package adapter

import (
	"strings"
	"testing"
)

func routed(name string, claims Claims) Registration {
	return Registration{Name: name, Claims: claims, Inputs: claims.Inputs}
}

func TestClaimRoutingRejectsPreciseOverlapAndNamesClaim(t *testing.T) {
	values := []Registration{
		routed("typescript", Claims{Inputs: Inputs{Extensions: []string{".ts"}}, Evidence: []string{"exact"}}),
		routed("other", Claims{Inputs: Inputs{Filenames: []string{"service.ts"}}, Evidence: []string{"syntactic"}}),
	}
	err := ValidateClaimOverlap(values)
	if err == nil || !strings.Contains(err.Error(), `"typescript" and "other"`) || !strings.Contains(err.Error(), "service.ts") {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestClaimRoutingRejectsSameTierWildcardOverlap(t *testing.T) {
	values := []Registration{
		routed("broad", Claims{Inputs: Inputs{Extensions: []string{".*"}}, Evidence: []string{"syntactic"}}),
		routed("lua", Claims{Inputs: Inputs{Extensions: []string{".lua"}}, Evidence: []string{"syntactic"}}),
	}
	if err := ValidateClaimOverlap(values); err == nil || !strings.Contains(err.Error(), "extension .*") {
		t.Fatalf("wildcard overlap = %v", err)
	}
}

func TestAutomaticClaimsReserveBuiltInGoInputs(t *testing.T) {
	registrations := []Registration{routed("other-go", Claims{Inputs: Inputs{Extensions: []string{".go"}}, Evidence: []string{"exact"}})}
	if err := ValidateAutomaticClaims(registrations); err == nil || !strings.Contains(err.Error(), "weave-go") {
		t.Fatalf("built-in Go overlap = %v", err)
	}
	fallback := []Registration{routed("ctags", Claims{Inputs: Inputs{Extensions: []string{".*"}}, Evidence: []string{"syntactic"}, Fallback: true})}
	if err := ValidateAutomaticClaims(fallback); err != nil {
		t.Fatalf("fallback conflicted with built-in Go: %v", err)
	}
}

func TestConditionalOverlapIsResolvedAgainstConcreteWorktree(t *testing.T) {
	values := []Registration{
		routed("left", Claims{Inputs: Inputs{Extensions: []string{".fixture"}, ProjectMarkers: []string{"left.project"}}, Evidence: []string{"exact"}}),
		routed("right", Claims{Inputs: Inputs{Extensions: []string{".fixture"}, ProjectMarkers: []string{"right.project"}}, Evidence: []string{"exact"}}),
	}
	if err := ValidateClaimOverlap(values); err != nil {
		t.Fatalf("disjoint conditional claims failed globally: %v", err)
	}
	routes, err := RouteInputs([]string{"left.project", "main.fixture"}, values)
	if err != nil || strings.Join(routes["left"], ",") != "main.fixture" || len(routes["right"]) != 0 {
		t.Fatalf("single-marker routes=%#v err=%v", routes, err)
	}
	if _, err := RouteInputs([]string{"left.project", "right.project", "main.fixture"}, values); err == nil || !strings.Contains(err.Error(), "main.fixture") {
		t.Fatalf("coactive conditional conflict = %v", err)
	}
}

func TestClaimRoutingUsesFallbackOnlyForOtherwiseUnclaimedInputs(t *testing.T) {
	values := []Registration{
		routed("python", Claims{Inputs: Inputs{Extensions: []string{".py"}, ProjectMarkers: []string{"pyproject.toml"}}, Evidence: []string{"exact"}}),
		routed("weave-go", Claims{Inputs: Inputs{Extensions: []string{".go"}}, Evidence: []string{"exact"}}),
		routed("ctags", Claims{Inputs: Inputs{Extensions: []string{".*"}}, Evidence: []string{"syntactic"}, Fallback: true}),
	}
	routes, err := RouteInputs([]string{"pyproject.toml", "src/main.py", "main.go", "deploy.lua"}, values)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(routes["python"], ","); got != "src/main.py" {
		t.Fatalf("python routes = %q", got)
	}
	if got := strings.Join(routes["ctags"], ","); got != "deploy.lua,pyproject.toml" {
		t.Fatalf("fallback routes = %q", got)
	}
	if got := strings.Join(routes["weave-go"], ","); got != "main.go" {
		t.Fatalf("Go routes = %q", got)
	}
}

func TestExplicitRegistrationOverridesOnlySameManagedName(t *testing.T) {
	managed := []Registration{routed("same", Claims{Inputs: Inputs{Extensions: []string{".a"}}, Evidence: []string{"exact"}})}
	explicit := []Registration{routed("same", Claims{Inputs: Inputs{Extensions: []string{".b"}}, Evidence: []string{"exact"}})}
	merged, err := MergeRegistrations(managed, explicit)
	if err != nil || len(merged) != 1 || merged[0].Claims.Inputs.Extensions[0] != ".b" {
		t.Fatalf("merged=%#v err=%v", merged, err)
	}
	explicit = append(explicit, routed("different", Claims{Inputs: Inputs{Extensions: []string{".b"}}, Evidence: []string{"exact"}}))
	if _, err := MergeRegistrations(managed, explicit); err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("cross-name overlap = %v", err)
	}
}
