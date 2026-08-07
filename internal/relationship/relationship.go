// Package relationship constructs normalized graph relationships for built-in
// providers and authored declarations.
package relationship

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
)

const maxText = 1 << 20

// Builder supplies ownership and evidence defaults shared by one producer.
// Endpoint discovery and deterministic ID construction remain the producer's
// responsibility.
type Builder struct {
	UnitID   string
	Provider string
	Evidence graph.Evidence
}

// Spec is the provider-specific portion of one directed relationship.
type Spec struct {
	ID         string
	From       string
	To         string
	Kind       graph.EdgeKind
	Evidence   graph.Evidence
	DocumentID string
	Range      graph.Range
}

// Build validates and constructs the same graph edge shape used by every
// ingestion path. An empty Spec evidence uses the builder default.
func (builder Builder) Build(spec Spec) (graph.Edge, error) {
	evidence := spec.Evidence
	if evidence == "" {
		evidence = builder.Evidence
	}
	for _, field := range []struct{ label, value string }{
		{"edge ID", spec.ID}, {"unit ID", builder.UnitID}, {"provider", builder.Provider},
		{"from endpoint", spec.From}, {"to endpoint", spec.To},
	} {
		if err := validText(field.label, field.value); err != nil {
			return graph.Edge{}, err
		}
	}
	if !graph.IsEdgeKind(spec.Kind) {
		return graph.Edge{}, fmt.Errorf("unknown relationship kind %q", spec.Kind)
	}
	if !graph.IsEvidence(evidence) {
		return graph.Edge{}, fmt.Errorf("unknown relationship evidence %q", evidence)
	}
	if spec.DocumentID != "" {
		if err := validText("document ID", spec.DocumentID); err != nil {
			return graph.Edge{}, err
		}
		if err := spec.Range.Validate(); err != nil {
			return graph.Edge{}, fmt.Errorf("relationship range: %w", err)
		}
	}
	return graph.Edge{
		ID: spec.ID, UnitID: builder.UnitID, From: spec.From, To: spec.To,
		Kind: spec.Kind, Evidence: evidence, DocumentID: spec.DocumentID,
		Range: spec.Range, Provider: builder.Provider,
	}, nil
}

// MustBuild is for built-in producers whose values are derived from already
// validated syntax and constants. User-authored input must call Build.
func (builder Builder) MustBuild(spec Spec) graph.Edge {
	edge, err := builder.Build(spec)
	if err != nil {
		panic("build internal relationship: " + err.Error())
	}
	return edge
}

func validText(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > maxText || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s is invalid or exceeds 1 MiB", label)
	}
	return nil
}
