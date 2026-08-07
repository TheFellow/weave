package application

import (
	"reflect"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
)

func TestImpactRootsResolveFilesPackagesAndReferencedSymbols(t *testing.T) {
	snapshot := graph.Snapshot{
		Documents: []graph.Document{{ID: "doc", UnitID: "unit", Path: "pkg/service.go"}},
		Symbols: []graph.Symbol{
			{ID: "package", UnitID: "unit", StableName: "example.test/pkg", DisplayName: "example.test/pkg", Kind: "package"},
			{ID: "defined", UnitID: "unit", DocumentID: "doc", DisplayName: "Defined"},
		},
		Occurrences: []graph.Occurrence{{ID: "occ", UnitID: "unit", DocumentID: "doc", SymbolID: "external"}},
	}
	roots, diagnostics, err := impactRoots(snapshot, []string{"pkg/service.go"}, []string{"example.test/pkg"})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("impactRoots error=%v diagnostics=%q", err, diagnostics)
	}
	if want := []string{"defined", "external", "package"}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots = %q, want %q", roots, want)
	}
}

func TestImpactRootsReportsUnmatchedInputsWithoutDiscardingMatches(t *testing.T) {
	snapshot := graph.Snapshot{Documents: []graph.Document{{ID: "doc", UnitID: "unit", Path: "found.go"}}, Symbols: []graph.Symbol{{ID: "found", UnitID: "unit", DocumentID: "doc"}}}
	roots, diagnostics, err := impactRoots(snapshot, []string{"missing.go", "found.go"}, nil)
	if err != nil || !reflect.DeepEqual(roots, []string{"found"}) || len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "file:missing.go") {
		t.Fatalf("roots=%q diagnostics=%q err=%v", roots, diagnostics, err)
	}
}

func TestImpactRootsIncludeWorkspaceFilesWithoutCompilerDocuments(t *testing.T) {
	snapshot := graph.Snapshot{Symbols: []graph.Symbol{{ID: "asset", StableName: "assets/diagram.svg", Kind: "asset"}}}
	roots, diagnostics, err := impactRoots(snapshot, []string{"assets/diagram.svg"}, nil)
	if err != nil || len(diagnostics) != 0 || !reflect.DeepEqual(roots, []string{"asset"}) {
		t.Fatalf("roots=%q diagnostics=%q err=%v", roots, diagnostics, err)
	}
}

func TestAffectedTestsRequiresGraphOrTestDocumentEvidence(t *testing.T) {
	snapshot := graph.Snapshot{
		Documents: []graph.Document{{ID: "go-test", Path: "service_test.go"}, {ID: "ordinary", Path: "service.go"}},
		Symbols: []graph.Symbol{
			{ID: "go", DisplayName: "TestService", DocumentID: "go-test"},
			{ID: "ordinary", DisplayName: "TestByNameOnly", DocumentID: "ordinary"},
			{ID: "declared", DisplayName: "ChecksService", DocumentID: "ordinary"},
		},
		Edges: []graph.Edge{{ID: "tests", From: "declared", To: "service", Kind: graph.EdgeTests}},
	}
	tests := affectedTests(snapshot, []string{"ordinary", "declared", "go"})
	if len(tests) != 2 || tests[0].ID != "declared" || tests[1].ID != "go" {
		t.Fatalf("affectedTests = %#v", tests)
	}
}
