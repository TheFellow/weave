// Package querysession exposes Weave's local graph as a persistent,
// language-neutral request stream for agents and other tools.
package querysession

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
)

const (
	Protocol       = "weave.query-session/v1"
	maxRequestSize = 1 << 20
)

// Request is one bounded local query. One JSON object occupies one line.
type Request struct {
	Protocol          string           `json:"protocol"`
	ID                string           `json:"id"`
	Command           string           `json:"command"`
	Arguments         []string         `json:"arguments,omitempty"`
	Limit             int              `json:"limit,omitempty"`
	MaxDepth          int              `json:"max_depth,omitempty"`
	MaxEdges          int              `json:"max_edges,omitempty"`
	Kinds             []graph.EdgeKind `json:"kinds,omitempty"`
	Direction         query.Direction  `json:"direction,omitempty"`
	ContextLines      int              `json:"context_lines,omitempty"`
	MaxSourceBytes    int              `json:"max_source_bytes,omitempty"`
	RelationshipLimit int              `json:"relationship_limit,omitempty"`
	ImpactFiles       []string         `json:"impact_files,omitempty"`
	ImpactPackages    []string         `json:"impact_packages,omitempty"`
	DiffRevision      string           `json:"diff_revision,omitempty"`
}

// Error is a protocol-safe query failure.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Frame is one response line. Exactly one of Result and Error is populated.
type Frame struct {
	Protocol string                `json:"protocol"`
	ID       string                `json:"id,omitempty"`
	Result   *application.Response `json:"result,omitempty"`
	Error    *Error                `json:"error,omitempty"`
}

// Serve reads NDJSON requests until EOF and writes one response frame for each
// line. Stdout must be reserved exclusively for these frames.
func Serve(ctx context.Context, service application.Service, input io.Reader, output io.Writer) error {
	if service == nil {
		return errors.New("query session service is nil")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxRequestSize)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var request Request
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			if encodeErr := encoder.Encode(errorFrame("", "invalid_request", "decode request: "+err.Error())); encodeErr != nil {
				return encodeErr
			}
			continue
		}
		invocation, err := request.invocation()
		if err != nil {
			if encodeErr := encoder.Encode(errorFrame(request.ID, "invalid_request", err.Error())); encodeErr != nil {
				return encodeErr
			}
			continue
		}
		result, err := service.Execute(ctx, invocation)
		if err != nil {
			if encodeErr := encoder.Encode(errorFrame(request.ID, "query_failed", err.Error())); encodeErr != nil {
				return encodeErr
			}
			continue
		}
		if err := encoder.Encode(Frame{Protocol: Protocol, ID: request.ID, Result: &result}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read query session request: %w", err)
	}
	return nil
}

func errorFrame(id, code, message string) Frame {
	message = strings.ToValidUTF8(message, "�")
	if len(message) > 8<<10 {
		message = message[:8<<10]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return Frame{Protocol: Protocol, ID: id, Error: &Error{Code: code, Message: message}}
}

func (request Request) invocation() (application.Invocation, error) {
	if request.Protocol != Protocol {
		return application.Invocation{}, fmt.Errorf("unsupported protocol %q; want %q", request.Protocol, Protocol)
	}
	if strings.TrimSpace(request.ID) == "" || len(request.ID) > 128 {
		return application.Invocation{}, errors.New("id must contain between 1 and 128 characters")
	}
	arities := map[string]int{
		"symbols": 1, "context": 1, "explore": 1, "definition": 1,
		"references": 1, "callers": 1, "callees": 1, "dependencies": 1,
		"path": 2, "graph": 1, "workspace find": 1, "workspace outline": 1,
		"workspace links": 1, "workspace backlinks": 1,
	}
	if request.Command == "impact" {
		if len(request.Arguments) > 1 || (len(request.Arguments) == 0 && len(request.ImpactFiles) == 0 && len(request.ImpactPackages) == 0 && request.DiffRevision == "") {
			return application.Invocation{}, errors.New("impact expects one symbol or at least one file, package, or diff revision")
		}
	} else if arity, ok := arities[request.Command]; !ok {
		return application.Invocation{}, fmt.Errorf("command %q is not a resident agent query", request.Command)
	} else if len(request.Arguments) != arity {
		return application.Invocation{}, fmt.Errorf("%s expects %d argument(s)", request.Command, arity)
	}
	for _, argument := range request.Arguments {
		if strings.TrimSpace(argument) == "" {
			return application.Invocation{}, errors.New("arguments must not be empty")
		}
	}
	for _, kind := range request.Kinds {
		if !graph.IsEdgeKind(kind) {
			return application.Invocation{}, fmt.Errorf("unknown edge kind %q", kind)
		}
	}
	limit := request.Limit
	if limit == 0 {
		limit = 50
		switch request.Command {
		case "context":
			limit = 16
		case "explore":
			limit = 8
		case "graph":
			limit = 100
		}
	}
	if limit < 1 || limit > 100000 {
		return application.Invocation{}, errors.New("limit must be between 1 and 100000")
	}
	maxDepth := request.MaxDepth
	if maxDepth == 0 {
		maxDepth = 8
	}
	if maxDepth < 1 || maxDepth > 100 {
		return application.Invocation{}, errors.New("max_depth must be between 1 and 100")
	}
	if request.MaxEdges < 0 || request.MaxEdges > 20000 {
		return application.Invocation{}, errors.New("max_edges must be between 1 and 20000 when set")
	}
	contextLines := request.ContextLines
	if contextLines == 0 && request.Command == "context" {
		contextLines = 2
	} else if contextLines < 0 || contextLines > 100 {
		return application.Invocation{}, errors.New("context_lines must be between 0 and 100")
	}
	maxSourceBytes := request.MaxSourceBytes
	if maxSourceBytes == 0 {
		maxSourceBytes = 64 << 10
	}
	if maxSourceBytes < 1 || maxSourceBytes > 4<<20 {
		return application.Invocation{}, errors.New("max_source_bytes must be between 1 and 4194304")
	}
	relationshipLimit := request.RelationshipLimit
	if relationshipLimit == 0 {
		relationshipLimit = 6
	}
	if relationshipLimit < 1 || relationshipLimit > 512 {
		return application.Invocation{}, errors.New("relationship_limit must be between 1 and 512")
	}
	direction := request.Direction
	if direction == "" {
		direction = query.DirectionBoth
	}
	if direction != query.DirectionIncoming && direction != query.DirectionOutgoing && direction != query.DirectionBoth {
		return application.Invocation{}, errors.New("direction must be incoming, outgoing, or both")
	}
	return application.Invocation{
		Command: request.Command, Arguments: append([]string(nil), request.Arguments...), JSON: true,
		Limit: limit, MaxDepth: maxDepth, MaxEdges: request.MaxEdges, Kinds: append([]graph.EdgeKind(nil), request.Kinds...),
		Direction: direction, Scope: "local", ContextLines: contextLines, MaxSourceBytes: maxSourceBytes,
		ContextLimit: relationshipLimit, ImpactFiles: append([]string(nil), request.ImpactFiles...),
		ImpactPackages: append([]string(nil), request.ImpactPackages...), DiffRevision: request.DiffRevision,
	}, nil
}
