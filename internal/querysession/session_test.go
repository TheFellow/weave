package querysession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/graph"
)

type recordingService struct {
	invocations []application.Invocation
	err         error
}

func TestCompactExplorePreservesExactFollowUpCoordinates(t *testing.T) {
	document := graph.Document{ID: "document", Path: "source.go"}
	source := contextquery.SourceExcerpt{Status: contextquery.SourceCurrent, Path: "source.go"}
	response := application.Response{Contexts: []contextquery.Result{{
		Evidence: []contextquery.Evidence{{Document: &document, Repositories: []contextquery.Repository{{Identity: "example/repo"}}, Source: source}},
		Incoming: []contextquery.Relationship{{
			Edge:     graph.Edge{ID: "edge", From: "caller", To: "focus", Kind: graph.EdgeCalls, Provider: "compiler", Evidence: graph.EvidenceExact},
			Entity:   &contextquery.Entity{Symbol: graph.Symbol{ID: "caller", StableName: "example.Caller", DisplayName: "Caller", Kind: "function", Provider: "compiler", Evidence: graph.EvidenceExact}},
			Document: &document, Repositories: []contextquery.Repository{{Identity: "example/repo"}}, Source: &source,
		}},
	}}}
	compactExplore(&response)
	result := response.Contexts[0]
	if result.Evidence[0].Document != nil || len(result.Evidence[0].Repositories) != 0 || result.Evidence[0].Source.Path != "source.go" {
		t.Fatalf("compact evidence = %#v", result.Evidence[0])
	}
	relationship := result.Incoming[0]
	if relationship.Entity != nil || relationship.Adjacent == nil || relationship.Adjacent.ID != "caller" || relationship.Adjacent.StableName != "example.Caller" || relationship.Edge.ID != "edge" {
		t.Fatalf("compact relationship = %#v", relationship)
	}
	if relationship.Document != nil || relationship.Source != nil || len(relationship.Repositories) != 0 {
		t.Fatalf("redundant relationship fields survived: %#v", relationship)
	}
}

func (service *recordingService) Execute(_ context.Context, invocation application.Invocation) (application.Response, error) {
	service.invocations = append(service.invocations, invocation)
	return application.Response{Schema: application.QuerySchema, Command: invocation.Command, Query: invocation.Arguments}, service.err
}

func TestServeExecutesMultipleBoundedQueries(t *testing.T) {
	input := strings.Join([]string{
		`{"protocol":"weave.query-session/v1","id":"one","command":"explore","arguments":["where are menu entries assembled"]}`,
		`{"protocol":"weave.query-session/v1","id":"two","command":"path","arguments":["a","b"],"max_depth":3,"limit":20}`,
	}, "\n") + "\n"
	service := &recordingService{}
	var output bytes.Buffer
	if err := Serve(context.Background(), service, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if len(service.invocations) != 2 || service.invocations[0].Limit != 8 || service.invocations[0].MaxSourceBytes != 32<<10 || service.invocations[0].ContextLimit != 4 || service.invocations[1].MaxDepth != 3 {
		t.Fatalf("invocations = %#v", service.invocations)
	}
	decoder := json.NewDecoder(&output)
	for _, wantID := range []string{"one", "two"} {
		var frame Frame
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Protocol != Protocol || frame.ID != wantID || frame.Result == nil || frame.Error != nil {
			t.Fatalf("frame = %#v", frame)
		}
	}
}

func TestServeKeepsSessionAliveAfterRequestErrors(t *testing.T) {
	input := strings.Join([]string{
		`not json`,
		`{"protocol":"wrong","id":"bad","command":"symbols","arguments":["x"],"limti":2}`,
		`{"protocol":"weave.query-session/v1","id":"good","command":"symbols","arguments":["x"]}`,
	}, "\n") + "\n"
	service := &recordingService{}
	var output bytes.Buffer
	if err := Serve(context.Background(), service, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var malformed, wrong, good Frame
	for _, frame := range []*Frame{&malformed, &wrong, &good} {
		if err := decoder.Decode(frame); err != nil {
			t.Fatal(err)
		}
	}
	if malformed.Error == nil || wrong.Error == nil || good.Result == nil || len(service.invocations) != 1 {
		t.Fatalf("frames = %#v %#v %#v; invocations=%#v", malformed, wrong, good, service.invocations)
	}
}

func TestGoldenRequestsAreLanguageNeutralContractFixtures(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("..", "..", "protocol", "query-session", "v1", "requests.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingService{}
	var output bytes.Buffer
	if err := Serve(context.Background(), service, bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if len(service.invocations) != 3 {
		t.Fatalf("golden invocations = %#v", service.invocations)
	}
	decoder := json.NewDecoder(&output)
	for index := 0; index < 3; index++ {
		var frame Frame
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Result == nil || frame.Error != nil {
			t.Fatalf("golden frame %d = %#v", index, frame)
		}
	}
}

func TestServeReturnsQueryFailureAsAFrame(t *testing.T) {
	service := &recordingService{err: errors.New("fixture failure")}
	var output bytes.Buffer
	input := `{"protocol":"weave.query-session/v1","id":"query","command":"symbols","arguments":["x"]}` + "\n"
	if err := Serve(context.Background(), service, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var frame Frame
	if err := json.Unmarshal(output.Bytes(), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Error == nil || frame.Error.Code != "query_failed" || !strings.Contains(frame.Error.Message, "fixture failure") {
		t.Fatalf("frame = %#v", frame)
	}
}
