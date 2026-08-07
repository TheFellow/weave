package architecture_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TheFellow/weave/internal/architecture"
	"github.com/TheFellow/weave/internal/graph"
)

func TestForbidAndAllowRulesWithExactEvidence(t *testing.T) {
	config := architecture.Config{
		Schema: architecture.Schema,
		Layers: []architecture.Layer{
			{ID: "api", Paths: []string{"internal/api/**"}},
			{ID: "storage", Units: []string{"example.com/app/storage"}},
			{ID: "contracts", Symbols: []string{"example.com/app/contracts.*"}},
		},
		Rules: []architecture.Rule{
			{ID: "api-no-storage", Action: "forbid", From: "api", To: "storage", Kinds: []graph.EdgeKind{graph.EdgeImports}, Message: "API cannot import storage"},
			{ID: "api-call-contracts", Action: "allow", From: "api", To: "contracts", Kinds: []graph.EdgeKind{graph.EdgeCalls}},
		},
	}
	snapshot := fixtureSnapshot()
	report := architecture.Check(config, snapshot)
	if len(report.Violations) != 2 {
		t.Fatalf("violations = %#v", report.Violations)
	}
	if got := []string{report.Violations[0].RuleID, report.Violations[1].RuleID}; !reflect.DeepEqual(got, []string{"api-call-contracts", "api-no-storage"}) {
		t.Fatalf("rule IDs = %#v", got)
	}
	for _, violation := range report.Violations {
		if violation.Document != "internal/api/handler.go" || violation.Range.Start.Line != 4 || violation.Provider != "fixture" || violation.Evidence != graph.EvidenceExact {
			t.Fatalf("evidence = %#v", violation)
		}
	}
	config.Rules = nil
	if got := architecture.Check(config, snapshot); len(got.Violations) != 0 {
		t.Fatalf("pass report = %#v", got)
	}
}

func TestLoadRejectsMalformedAndUnknownConfiguration(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := architecture.Load(missing); !errors.Is(err, architecture.ErrNotConfigured) {
		t.Fatalf("missing error = %v", err)
	}
	tests := []string{
		`{"schema":"wrong","layers":[],"rules":[]}`,
		`{"schema":"weave.architecture/v1","unknown":true,"layers":[],"rules":[]}`,
		`{"schema":"weave.architecture/v1","layers":[{"id":"x","paths":["["]}],"rules":[]}`,
		`{"schema":"weave.architecture/v1","layers":[],"rules":[]} {}`,
	}
	for _, input := range tests {
		path := filepath.Join(t.TempDir(), "architecture.json")
		if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := architecture.Load(path); err == nil {
			t.Fatalf("Load(%s) error = nil", input)
		}
	}
}

func TestDeclaredBridgeToExternalSymbolParticipatesInPolicy(t *testing.T) {
	config := architecture.Config{
		Schema: architecture.Schema,
		Layers: []architecture.Layer{
			{ID: "service", Symbols: []string{"service.Handler"}},
			{ID: "forbidden-contract", Symbols: []string{"scip openapi contracts 1 Legacy#"}},
		},
		Rules: []architecture.Rule{{
			ID: "no-legacy-contract", Action: "forbid", From: "service", To: "forbidden-contract",
			Kinds: []graph.EdgeKind{graph.EdgeDependsOn},
		}},
	}
	report := architecture.Check(config, graph.Snapshot{
		Symbols: []graph.Symbol{{ID: "service.Handler", StableName: "service.Handler"}},
		Edges: []graph.Edge{{
			ID: "bridge", From: "service.Handler", To: "scip openapi contracts 1 Legacy#",
			Kind: graph.EdgeDependsOn, Provider: "weave-bridges", Evidence: graph.EvidenceDeclared,
		}},
	})
	if len(report.Violations) != 1 || report.Violations[0].Evidence != graph.EvidenceDeclared || report.Violations[0].Provider != "weave-bridges" {
		t.Fatalf("bridge policy report = %#v", report)
	}
}

func TestSARIF21IsValidDeterministicJSON(t *testing.T) {
	config := architecture.Config{Schema: architecture.Schema, Layers: []architecture.Layer{{ID: "api", Paths: []string{"internal/api/**"}}, {ID: "storage", Units: []string{"example.com/app/storage"}}}, Rules: []architecture.Rule{{ID: "api-no-storage", Action: "forbid", From: "api", To: "storage", Kinds: []graph.EdgeKind{graph.EdgeImports}}}}
	report := architecture.Check(config, fixtureSnapshot())
	log := architecture.SARIF(config, report)
	content, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["version"] != "2.1.0" || decoded["$schema"] != architecture.SARIFSchema {
		t.Fatalf("SARIF header = %s", content)
	}
	location := log.Runs[0].Results[0].Locations[0].PhysicalLocation
	if location.ArtifactLocation.URI != "internal/api/handler.go" || location.Region.StartLine != 5 || location.Region.StartColumn != 3 {
		t.Fatalf("location = %#v", location)
	}
}

func fixtureSnapshot() graph.Snapshot {
	rng := graph.Range{Start: graph.Position{Line: 4, Column: 2, Byte: 42}, End: graph.Position{Line: 4, Column: 12, Byte: 52}}
	return graph.Snapshot{
		Documents: []graph.Document{{ID: "api-doc", UnitID: "example.com/app/api", Path: "internal/api/handler.go"}},
		Symbols: []graph.Symbol{
			{ID: "api.Handler", UnitID: "example.com/app/api", StableName: "example.com/app/api.Handler", DocumentID: "api-doc"},
			{ID: "storage.Save", UnitID: "example.com/app/storage", StableName: "example.com/app/storage.Save"},
			{ID: "other.Call", UnitID: "example.com/app/other", StableName: "example.com/app/other.Call"},
		},
		Edges: []graph.Edge{
			{ID: "call", From: "api.Handler", To: "other.Call", Kind: graph.EdgeCalls, DocumentID: "api-doc", Range: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
			{ID: "import", From: "api.Handler", To: "storage.Save", Kind: graph.EdgeImports, DocumentID: "api-doc", Range: rng, Provider: "fixture", Evidence: graph.EvidenceExact},
		},
	}
}
