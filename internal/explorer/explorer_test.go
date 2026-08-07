package explorer

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/contextquery"
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
	if len(result.Edges) != 1 || result.Edges[0].SVGID != dot.EdgeSVGID(result.Edges[0].Facts[0]) {
		t.Fatalf("edge mappings = %#v", result.Edges)
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

func TestResultEdgesPreservesCollapsedFactsUnderStableIdentity(t *testing.T) {
	t.Parallel()
	first := graph.Edge{ID: "first", From: "a", To: "b", Kind: graph.EdgeCalls, Provider: "fixture", Evidence: graph.EvidenceExact}
	second := first
	second.ID = "second"
	result := resultEdges([]graph.Edge{second, first})
	if len(result) != 1 || result[0].SVGID != dot.EdgeSVGID(first) || len(result[0].Facts) != 2 || result[0].Facts[0].ID != "first" {
		t.Fatalf("collapsed edge mapping = %#v", result)
	}
}

func TestEngineUsesCanonicalContextAndLinkApplicationContracts(t *testing.T) {
	t.Parallel()
	relationship := contextquery.Relationship{Edge: graph.Edge{
		ID: "edge-id", From: "focus", To: "dependency", Kind: graph.EdgeCalls,
		Provider: "fixture", Evidence: graph.EvidenceExact,
	}}
	contextResult := contextquery.Result{
		Schema:   contextquery.Schema,
		Focus:    contextquery.Entity{Symbol: graph.Symbol{ID: "focus", DisplayName: "Focus"}},
		Outgoing: []contextquery.Relationship{relationship},
	}
	revision := "sha256:" + strings.Repeat("a", 64)
	service := &routingService{execute: func(invocation application.Invocation) (application.Response, error) {
		switch invocation.Command {
		case "context":
			return application.Response{Command: "context", Context: &contextResult}, nil
		case "links list":
			return application.Response{Command: "links list", LinkRevision: revision}, nil
		case "links add":
			return application.Response{Command: "links add", LinkRevision: "sha256:" + strings.Repeat("b", 64), Links: []bridge.Link{{ID: "docs-code"}}}, nil
		default:
			return application.Response{}, nil
		}
	}}
	base := application.Invocation{Arguments: []string{"Initial"}, Scope: "catalog", MaxRepos: 4}
	engine, err := New(service, base)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := engine.Detail(context.Background(), DetailRequest{Target: "focus", EdgeID: "edge-id", Limit: 12, ContextLines: 3, MaxSourceBytes: 8192})
	if err != nil || detail.Kind != "edge" || detail.Context != nil || detail.Relationship == nil || detail.Relationship.Edge.ID != "edge-id" {
		t.Fatalf("edge detail = %#v, %v", detail, err)
	}
	encodedDetail, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedDetail), `"context"`) {
		t.Fatalf("edge detail serialized surrounding context: %s", encodedDetail)
	}
	listed, err := engine.Links(context.Background())
	if err != nil || listed.Revision != revision {
		t.Fatalf("links = %#v, %v", listed, err)
	}
	from, to, kind, note := "entity:focus", "entity:dependency", graph.EdgeDocuments, "why"
	mutated, err := engine.MutateLink(context.Background(), "add", LinkMutation{
		ID: "docs-code", Revision: revision, From: &from, To: &to, Kind: &kind, Note: &note,
	})
	if err != nil || mutated.Operation != "add" || mutated.Link.ID != "docs-code" || mutated.Revision == revision {
		t.Fatalf("mutated links = %#v, %v", mutated, err)
	}
	invocations := service.Invocations()
	if len(invocations) != 3 {
		t.Fatalf("invocations = %#v", invocations)
	}
	contextInvocation := invocations[0]
	if contextInvocation.Command != "context" || !reflect.DeepEqual(contextInvocation.Arguments, []string{"focus"}) || contextInvocation.Limit != 12 || contextInvocation.ContextLines != 3 || contextInvocation.MaxSourceBytes != 8192 || contextInvocation.Scope != "catalog" {
		t.Fatalf("context invocation = %#v", contextInvocation)
	}
	linkInvocation := invocations[2]
	if linkInvocation.Command != "links add" || linkInvocation.LinkRevision != revision || !linkInvocation.LinkFromSet || linkInvocation.LinkFrom != from || !linkInvocation.LinkToSet || linkInvocation.LinkTo != to || !linkInvocation.LinkKindSet || linkInvocation.LinkKind != kind || !linkInvocation.LinkNoteSet || linkInvocation.LinkNote != note {
		t.Fatalf("link invocation = %#v", linkInvocation)
	}
}

func TestEngineRejectsStaleEdgeAndInvalidMutationBeforeWriting(t *testing.T) {
	t.Parallel()
	contextResult := contextquery.Result{Schema: contextquery.Schema, Focus: contextquery.Entity{Symbol: graph.Symbol{ID: "focus"}}}
	service := &routingService{execute: func(invocation application.Invocation) (application.Response, error) {
		return application.Response{Command: invocation.Command, Context: &contextResult}, nil
	}}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Detail(context.Background(), DetailRequest{Target: "focus", EdgeID: "gone"}); err == nil || !strings.Contains(err.Error(), "no longer") {
		t.Fatalf("stale edge error = %v", err)
	}
	from, to, kind := "entity:a", "entity:b", graph.EdgeLinksTo
	if _, err := engine.MutateLink(context.Background(), "add", LinkMutation{ID: "x", Revision: "stale", From: &from, To: &to, Kind: &kind}); err == nil {
		t.Fatal("invalid revision mutation succeeded")
	}
	if got := service.Invocations(); len(got) != 1 {
		t.Fatalf("invalid mutation reached application: %#v", got)
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

type routingService struct {
	mu          sync.Mutex
	execute     func(application.Invocation) (application.Response, error)
	invocations []application.Invocation
}

func (service *routingService) Execute(_ context.Context, invocation application.Invocation) (application.Response, error) {
	service.mu.Lock()
	service.invocations = append(service.invocations, invocation)
	service.mu.Unlock()
	return service.execute(invocation)
}

func (service *routingService) Invocations() []application.Invocation {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]application.Invocation(nil), service.invocations...)
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
