package explorer

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/dot"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
	"github.com/TheFellow/weave/internal/query"
)

func TestEngineUsesGraphApplicationContractAndRendersStableTargets(t *testing.T) {
	t.Parallel()
	service := &fixtureService{response: graphResponse()}
	base := application.Invocation{
		Arguments: []string{"Initial"}, Limit: 20, MaxDepth: 2, MaxEdges: 80,
		Scope: "catalog", Repositories: []string{"github.com/acme/service"}, MaxRepos: 4,
	}
	engine, err := New(service, base)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Query(context.Background(), Request{
		Target: "Focus", Direction: query.DirectionOutgoing,
		MaxDepth: 4, Limit: 25, MaxEdges: 90, Kinds: []graph.EdgeKind{graph.EdgeCalls},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantInvocation := base
	wantInvocation.Command = "graph"
	wantInvocation.Arguments = []string{"Focus"}
	wantInvocation.Direction = query.DirectionOutgoing
	wantInvocation.MaxDepth = 4
	wantInvocation.Limit = 25
	wantInvocation.MaxEdges = 90
	wantInvocation.Kinds = []graph.EdgeKind{graph.EdgeCalls}
	if got := service.Invocations(); !reflect.DeepEqual(got, []application.Invocation{wantInvocation}) {
		t.Fatalf("invocations = %#v, want %#v", got, wantInvocation)
	}
	if result.Schema != Schema || result.Focus != "focus" || result.FocusLabel != "Focus" || len(result.Nodes) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Nodes[0].SVGID != dot.NodeSVGID("focus") || result.Nodes[0].ID != "focus" {
		t.Fatalf("focus mapping = %#v", result.Nodes[0])
	}
	for _, want := range []string{`id="` + dot.NodeSVGID("focus") + `"`, `id="weave-edge-`, `id="weave-cluster-`} {
		if !strings.Contains(result.DOT, want) {
			t.Errorf("DOT omitted %q:\n%s", want, result.DOT)
		}
	}
	if strings.Contains(result.DOT, "Weave ·") {
		t.Fatalf("embedded DOT duplicated the explorer title:\n%s", result.DOT)
	}
	if !reflect.DeepEqual(result.Options.Providers, []string{"heuristic", "scip:fixture"}) {
		t.Fatalf("provider options = %#v", result.Options.Providers)
	}
}

func TestEngineExposesSnapshotDiffTransitionContract(t *testing.T) {
	t.Parallel()
	transitions := graphdiff.TransitionSet{Nodes: []graphdiff.Transition{{ID: "symbol", Status: "changed"}}}
	diff := graphdiff.Result{
		Schema:      graphdiff.Schema,
		Baseline:    graphdiff.Identity{Revision: "main", SnapshotDigest: "sha256:a"},
		Head:        graphdiff.Identity{Revision: "worktree", SnapshotDigest: "sha256:b"},
		Transitions: &transitions,
	}
	service := &fixtureService{response: application.Response{Command: "diff graph", Diff: &diff}}
	base := application.Invocation{Arguments: []string{"Initial"}, Scope: "local"}
	engine, err := New(service, base)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Diff(context.Background(), DiffRequest{Base: "main", Limit: 25, MaxDepth: 4, MaxEdges: 80, Kinds: []graph.EdgeKind{graph.EdgeCalls}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Transitions == nil || !reflect.DeepEqual(*result.Transitions, transitions) {
		t.Fatalf("transition result = %#v", result)
	}
	want := base
	want.Command = "diff graph"
	want.Arguments = nil
	want.JSON = true
	want.DiffBase = "main"
	want.Limit, want.MaxDepth, want.MaxEdges = 25, 4, 80
	want.Kinds = []graph.EdgeKind{graph.EdgeCalls}
	if got := service.Invocations(); !reflect.DeepEqual(got, []application.Invocation{want}) {
		t.Fatalf("invocations = %#v, want %#v", got, want)
	}
}

func TestEnginePrunesFilteredComponentsDisconnectedFromFocus(t *testing.T) {
	t.Parallel()
	response := application.Response{
		Command: "graph", Nodes: []string{"focus", "bridge", "leaf"},
		Symbols: []graph.Symbol{
			{ID: "focus", DisplayName: "Focus", Provider: "fixture"},
			{ID: "bridge", DisplayName: "Bridge", Provider: "fixture"},
			{ID: "leaf", DisplayName: "Leaf", Provider: "fixture"},
		},
		Edges: []graph.Edge{
			{ID: "filtered-bridge", From: "focus", To: "bridge", Kind: graph.EdgeCalls, Provider: "exact", Evidence: graph.EvidenceExact},
			{ID: "stranded-survivor", From: "bridge", To: "leaf", Kind: graph.EdgeCalls, Provider: "inferred", Evidence: graph.EvidenceInferred},
		},
	}
	service := &fixtureService{response: response}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Query(context.Background(), Request{
		Target: "focus", Direction: query.DirectionOutgoing, MaxDepth: 3, Limit: 10, MaxEdges: 20,
		Providers: []string{"inferred"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].ID != "focus" {
		t.Fatalf("filtered nodes = %#v, want only focus", result.Nodes)
	}
	if strings.Contains(result.DOT, "stranded-survivor") || strings.Contains(result.DOT, "Bridge") || strings.Contains(result.DOT, "Leaf") {
		t.Fatalf("filtered DOT retained disconnected component:\n%s", result.DOT)
	}
}

func TestEngineRejectsUnboundedOrUnknownRequests(t *testing.T) {
	t.Parallel()
	service := &fixtureService{response: graphResponse()}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []Request{
		{Target: ""},
		{Target: "focus", Direction: "sideways"},
		{Target: "focus", MaxDepth: 101},
		{Target: "focus", Limit: 5001},
		{Target: "focus", MaxEdges: 20001},
		{Target: "focus", Kinds: []graph.EdgeKind{"magic"}},
		{Target: "focus", Evidence: []graph.Evidence{"probably"}},
		{Target: "focus", Providers: []string{"bad\nprovider"}},
	}
	for _, request := range tests {
		if _, err := engine.Query(context.Background(), request); err == nil {
			t.Errorf("Query(%#v) succeeded", request)
		}
	}
	if got := service.Invocations(); len(got) != 0 {
		t.Fatalf("invalid requests invoked application: %#v", got)
	}
}

func graphResponse() application.Response {
	return application.Response{
		Schema: application.QuerySchema, Command: "graph", Truncated: true,
		Nodes: []string{"focus", "dependency", "caller"},
		Symbols: []graph.Symbol{
			{ID: "focus", StableName: "example.Focus", DisplayName: "Focus", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "dependency", StableName: "example.Dependency", DisplayName: "Dependency", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "caller", StableName: "example.Caller", DisplayName: "Caller", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
		},
		Edges: []graph.Edge{
			{ID: "out", From: "focus", To: "dependency", Kind: graph.EdgeCalls, Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "in", From: "caller", To: "focus", Kind: graph.EdgeCalls, Provider: "heuristic", Evidence: graph.EvidenceInferred},
		},
		Diagnostics: []string{"fixture diagnostic"},
	}
}

type fixtureService struct {
	mu          sync.Mutex
	response    application.Response
	err         error
	invocations []application.Invocation
}

func (service *fixtureService) Execute(_ context.Context, invocation application.Invocation) (application.Response, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.invocations = append(service.invocations, invocation)
	return service.response, service.err
}

func (service *fixtureService) Invocations() []application.Invocation {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]application.Invocation(nil), service.invocations...)
}
