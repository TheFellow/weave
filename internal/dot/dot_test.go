package dot_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/dot"
	"github.com/TheFellow/weave/internal/graph"
)

func TestWriteProducesFocusedClusteredDeterministicDOT(t *testing.T) {
	t.Parallel()
	view := dot.View{
		Focus: "target",
		Nodes: []string{"target", "upstream", "downstream", "external\\N\""},
		Symbols: []graph.Symbol{
			{ID: "target", StableName: "example/service.Target", DisplayName: "Target", Kind: "class", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "upstream", StableName: "example/api.Handler", DisplayName: "Handler", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "downstream", StableName: "docs/design.md", DisplayName: "Design", Kind: "document", Provider: "weave-workspace", Evidence: graph.EvidenceSyntactic},
		},
		Edges: []graph.Edge{
			{ID: "documents", From: "downstream", To: "target", Kind: graph.EdgeDocuments, Evidence: graph.EvidenceDeclared, Provider: "weave-bridges"},
			{ID: "calls", From: "upstream", To: "target", Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "scip:fixture"},
			{ID: "calls-again", From: "upstream", To: "target", Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "scip:fixture"},
			{ID: "external", From: "target", To: "external\\N\"", Kind: graph.EdgeDependsOn, Evidence: graph.EvidenceInferred, Provider: "fixture"},
		},
		Truncated: true,
	}
	var first, second bytes.Buffer
	if err := dot.Write(&first, view); err != nil {
		t.Fatal(err)
	}
	if err := dot.Write(&second, view); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("DOT output is not deterministic")
	}
	for _, value := range []string{
		"digraph weave {", `rankdir=LR`, `label="Weave · Target\nbounded result`,
		`label="scip:fixture"`, `fillcolor="#F6C85F"`, `fillcolor="#D9EAD3"`,
		`label="documents"`, `label="calls ×2"`, `2 equivalent source edges`, `color="#008C95"`, `style="rounded,filled,dashed"`,
		`external\\N\"`,
	} {
		if !strings.Contains(first.String(), value) {
			t.Errorf("DOT output does not contain %q:\n%s", value, first.String())
		}
	}
}

func TestWriteProducesGraphvizParseableOutputWhenAvailable(t *testing.T) {
	dotPath, err := exec.LookPath("dot")
	if err != nil {
		t.Skip("Graphviz dot is not installed")
	}
	var source bytes.Buffer
	err = dot.Write(&source, dot.View{
		Focus: "a",
		Nodes: []string{"a", "b"},
		Symbols: []graph.Symbol{
			{ID: "a", StableName: "a", DisplayName: "A <&>", Kind: "function", Provider: "test"},
			{ID: "b", StableName: "b", DisplayName: "B\nsecond line", Kind: "function", Provider: "test"},
		},
		Edges: []graph.Edge{{ID: "ab", From: "a", To: "b", Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact}},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(dotPath, "-Tdot")
	command.Stdin = bytes.NewReader(source.Bytes())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Graphviz rejected DOT: %v\n%s\nsource:\n%s", err, output, source.String())
	}
}

func TestWriteRejectsMissingFocus(t *testing.T) {
	if err := dot.Write(&bytes.Buffer{}, dot.View{}); err == nil {
		t.Fatal("empty focus succeeded")
	}
}

func TestWriteUsesStableSemanticNodeAndCollapsedEdgeIDs(t *testing.T) {
	t.Parallel()
	edge := graph.Edge{ID: "volatile-source-edge", From: "focus", To: "dependency", Kind: graph.EdgeCalls, Evidence: graph.EvidenceExact, Provider: "scip:fixture"}
	view := dot.View{
		Focus: "focus", Nodes: []string{"focus", "dependency"},
		Symbols: []graph.Symbol{
			{ID: "focus", StableName: "example.Focus", DisplayName: "Focus", Provider: "scip:fixture"},
			{ID: "dependency", StableName: "example.Dependency", DisplayName: "Dependency", Provider: "scip:fixture"},
		},
		Edges: []graph.Edge{edge},
	}
	var before bytes.Buffer
	if err := dot.Write(&before, view); err != nil {
		t.Fatal(err)
	}
	view.Nodes = append(view.Nodes, "a-new-sorted-node")
	view.Symbols = append(view.Symbols, graph.Symbol{ID: "a-new-sorted-node", DisplayName: "A", Provider: "another-provider"})
	var after bytes.Buffer
	if err := dot.Write(&after, view); err != nil {
		t.Fatal(err)
	}
	nodeDigest := shortDigest("focus")
	edgeDigest := shortDigest(strings.Join([]string{edge.From, edge.To, string(edge.Kind), string(edge.Evidence), edge.Provider}, "\x00"))
	for _, output := range []string{before.String(), after.String()} {
		for _, want := range []string{
			"n_" + nodeDigest + " [",
			`id="weave-node-` + nodeDigest + `"`,
			`id="weave-edge-` + edgeDigest + `"`,
		} {
			if !strings.Contains(output, want) {
				t.Fatalf("DOT output omitted stable identity %q:\n%s", want, output)
			}
		}
	}
}

func shortDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}
