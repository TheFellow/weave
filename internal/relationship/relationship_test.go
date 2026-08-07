package relationship_test

import (
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/relationship"
)

func TestBuilderAppliesProvenanceAndOptionalSource(t *testing.T) {
	source := graph.Range{Start: graph.Position{Line: 2, Column: 3, Byte: 8}, End: graph.Position{Line: 2, Column: 7, Byte: 12}}
	edge, err := (relationship.Builder{UnitID: "unit", Provider: "provider", Evidence: graph.EvidenceExact}).Build(relationship.Spec{
		ID: "edge", From: "document", To: "code", Kind: graph.EdgeDocuments,
		DocumentID: "source", Range: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if edge.UnitID != "unit" || edge.Provider != "provider" || edge.Evidence != graph.EvidenceExact || edge.DocumentID != "source" || edge.Range != source {
		t.Fatalf("edge = %#v", edge)
	}
}

func TestBuilderRejectsInvalidRelationships(t *testing.T) {
	builder := relationship.Builder{UnitID: "unit", Provider: "provider", Evidence: graph.EvidenceDeclared}
	for _, spec := range []relationship.Spec{
		{ID: "", From: "a", To: "b", Kind: graph.EdgeDocuments},
		{ID: "edge", From: "", To: "b", Kind: graph.EdgeDocuments},
		{ID: "edge", From: "a", To: "b", Kind: "mystery"},
		{ID: "edge", From: "a", To: "b", Kind: graph.EdgeDocuments, Evidence: "wishful"},
		{ID: "edge", From: "a", To: "b", Kind: graph.EdgeDocuments, DocumentID: "doc", Range: graph.Range{Start: graph.Position{Line: 2}, End: graph.Position{Line: 1}}},
	} {
		if _, err := builder.Build(spec); err == nil {
			t.Fatalf("Build(%#v) succeeded", spec)
		}
	}
}
