package graph_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
)

func TestTokensSplitIdentifiersDeterministically(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "HandleHTTPRequest2", want: []string{"2", "handle", "http", "request"}},
		{name: "parse_user-ID", want: []string{"id", "parse", "user"}},
		{name: "HTTPServerHTTP", want: []string{"http", "server"}},
	} {
		if got := graph.Tokens(test.name); !reflect.DeepEqual(got, test.want) {
			t.Errorf("Tokens(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestExtractSearchTermsBoundsAndNormalizesContent(t *testing.T) {
	t.Parallel()
	got := graph.ExtractSearchTerms("Latency alone is insufficient; retained ARTIFACTS make correctness auditable. x " + strings.Repeat("z", 129))
	want := []string{"alone", "artifacts", "auditable", "correctness", "insufficient", "latency", "make", "retained"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractSearchTerms() = %q, want %q", got, want)
	}
}

func TestUnitFactsValidationRejectsInvalidSearchTerms(t *testing.T) {
	facts := graph.UnitFacts{
		Unit: graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{{
			ID: "symbol", UnitID: "unit", StableName: "Symbol", DisplayName: "Symbol", SearchTerms: []string{"Not-Normalized"},
			Kind: "section", Provider: "fixture", Evidence: graph.EvidenceSyntactic,
		}},
	}
	if err := facts.Validate(); err == nil || !strings.Contains(err.Error(), "search term") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestUnitFactsValidationRejectsCrossUnitDocument(t *testing.T) {
	t.Parallel()
	facts := graph.UnitFacts{
		Unit:      graph.Unit{ID: "a", Provider: "fixture", ProviderVersion: "1"},
		Documents: []graph.Document{{ID: "doc", UnitID: "b", Path: "a.go", Provider: "fixture"}},
	}
	if err := facts.Validate(); err == nil {
		t.Fatal("Validate() error = nil")
	}
}

func TestUnitFactsValidationEnforcesProviderOwnership(t *testing.T) {
	t.Parallel()
	base := graph.UnitFacts{
		Unit: graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Documents: []graph.Document{{
			ID: "doc", UnitID: "unit", Path: "source.go", Language: "go",
			Provider: "fixture", ProviderVersion: "1",
		}},
		Symbols: []graph.Symbol{{
			ID: "symbol", UnitID: "unit", StableName: "fixture Symbol", DisplayName: "Symbol",
			Kind: "function", DocumentID: "doc", Provider: "fixture", Evidence: graph.EvidenceExact,
		}},
		Occurrences: []graph.Occurrence{{
			ID: "occurrence", UnitID: "unit", SymbolID: "external:symbol", DocumentID: "doc",
			Role: "reference", Provider: "fixture", Evidence: graph.EvidenceExact,
		}},
		Edges: []graph.Edge{{
			ID: "edge", UnitID: "unit", From: "symbol", To: "external:symbol",
			Kind: graph.EdgeReferences, Provider: "fixture", Evidence: graph.EvidenceExact,
		}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid open-endpoint enrichment: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*graph.UnitFacts)
		want   string
	}{
		{"document provider", func(facts *graph.UnitFacts) { facts.Documents[0].Provider = "spoofed" }, "document"},
		{"document provider version", func(facts *graph.UnitFacts) { facts.Documents[0].ProviderVersion = "2" }, "document"},
		{"symbol provider", func(facts *graph.UnitFacts) { facts.Symbols[0].Provider = "spoofed" }, "symbol"},
		{"occurrence provider", func(facts *graph.UnitFacts) { facts.Occurrences[0].Provider = "spoofed" }, "occurrence"},
		{"edge provider", func(facts *graph.UnitFacts) { facts.Edges[0].Provider = "spoofed" }, "edge"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := base
			facts.Documents = append([]graph.Document(nil), base.Documents...)
			facts.Symbols = append([]graph.Symbol(nil), base.Symbols...)
			facts.Occurrences = append([]graph.Occurrence(nil), base.Occurrences...)
			facts.Edges = append([]graph.Edge(nil), base.Edges...)
			test.mutate(&facts)
			if err := facts.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %s ownership error", err, test.want)
			}
		})
	}
}
