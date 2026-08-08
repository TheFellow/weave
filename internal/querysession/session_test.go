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
)

type recordingService struct {
	invocations []application.Invocation
	err         error
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
	if len(service.invocations) != 2 || service.invocations[0].Limit != 8 || service.invocations[0].MaxSourceBytes != 64<<10 || service.invocations[1].MaxDepth != 3 {
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
