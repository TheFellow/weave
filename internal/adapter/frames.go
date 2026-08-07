package adapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
)

type frame struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

type runBegin struct {
	Provider     Provider `json:"provider"`
	FactEncoding string   `json:"fact_encoding"`
}
type unitBegin struct {
	Unit graph.Unit `json:"unit"`
}
type factBatch struct {
	Documents   []graph.Document   `json:"documents,omitempty"`
	Symbols     []graph.Symbol     `json:"symbols,omitempty"`
	Occurrences []graph.Occurrence `json:"occurrences,omitempty"`
	Edges       []graph.Edge       `json:"edges,omitempty"`
}
type counts struct {
	Documents   int `json:"documents"`
	Symbols     int `json:"symbols"`
	Occurrences int `json:"occurrences"`
	Edges       int `json:"edges"`
}
type unitEnd struct {
	Status string `json:"status"`
	Counts counts `json:"counts"`
}
type runEnd struct {
	Status string   `json:"status"`
	Units  []string `json:"units"`
}

func parseFrames(reader io.Reader, requestID string, capabilities Capabilities, limits Limits) (Result, error) {
	buffered := bufio.NewReaderSize(reader, min(int(limits.MaxFrameBytes), 64<<10))
	result := Result{Provider: capabilities.Provider}
	units := map[string]*graph.UnitFacts{}
	var current *graph.UnitFacts
	var total int64
	frames, facts := 0, 0
	begun, ended := false, false
	for {
		line, readErr := readFrame(buffered, &total, limits)
		if len(line) > 0 {
			frames++
			if frames > limits.MaxFrames {
				return Result{}, errors.New("adapter frame count exceeds limit")
			}
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 || !utf8.Valid(line) {
				return Result{}, fmt.Errorf("frame %d is blank or invalid UTF-8", frames)
			}
			var envelope frame
			if err := decodeStrict(line, &envelope); err != nil {
				return Result{}, fmt.Errorf("frame %d: %w", frames, err)
			}
			if envelope.Protocol != Protocol || envelope.RequestID != requestID {
				return Result{}, fmt.Errorf("frame %d has mismatched protocol or request ID", frames)
			}
			if ended {
				return Result{}, errors.New("frame follows run.end")
			}
			switch envelope.Kind {
			case "run.begin":
				if begun || current != nil || len(units) != 0 {
					return Result{}, errors.New("duplicate or misplaced run.begin")
				}
				var payload runBegin
				if err := decodeStrict(envelope.Payload, &payload); err != nil {
					return Result{}, fmt.Errorf("run.begin: %w", err)
				}
				if payload.Provider != capabilities.Provider || payload.FactEncoding != FactEncoding {
					return Result{}, errors.New("run.begin does not match negotiated capabilities")
				}
				begun = true
			case "unit.begin":
				if !begun || current != nil {
					return Result{}, errors.New("misplaced unit.begin")
				}
				var payload unitBegin
				if err := decodeStrict(envelope.Payload, &payload); err != nil {
					return Result{}, fmt.Errorf("unit.begin: %w", err)
				}
				if payload.Unit.ID == "" || units[payload.Unit.ID] != nil {
					return Result{}, fmt.Errorf("duplicate or empty unit %q", payload.Unit.ID)
				}
				if payload.Unit.Provider != capabilities.Provider.Name || payload.Unit.ProviderVersion != capabilities.Provider.Version {
					return Result{}, fmt.Errorf("unit %q provider does not match run", payload.Unit.ID)
				}
				current = &graph.UnitFacts{Unit: payload.Unit}
				units[payload.Unit.ID] = current
			case "facts":
				if current == nil {
					return Result{}, errors.New("facts outside a unit")
				}
				var payload factBatch
				if err := decodeStrict(envelope.Payload, &payload); err != nil {
					return Result{}, fmt.Errorf("facts: %w", err)
				}
				facts += len(payload.Documents) + len(payload.Symbols) + len(payload.Occurrences) + len(payload.Edges)
				if facts > limits.MaxFacts {
					return Result{}, errors.New("adapter fact count exceeds limit")
				}
				current.Documents = append(current.Documents, payload.Documents...)
				current.Symbols = append(current.Symbols, payload.Symbols...)
				current.Occurrences = append(current.Occurrences, payload.Occurrences...)
				current.Edges = append(current.Edges, payload.Edges...)
			case "unit.end":
				if current == nil {
					return Result{}, errors.New("unit.end outside a unit")
				}
				var payload unitEnd
				if err := decodeStrict(envelope.Payload, &payload); err != nil {
					return Result{}, fmt.Errorf("unit.end: %w", err)
				}
				if payload.Status != "complete" || payload.Counts != factCounts(*current) {
					return Result{}, fmt.Errorf("unit %q incomplete or count mismatch", current.Unit.ID)
				}
				if err := current.Validate(); err != nil {
					return Result{}, fmt.Errorf("unit %q: %w", current.Unit.ID, err)
				}
				result.Units = append(result.Units, *current)
				current = nil
			case "diagnostic":
				if !begun {
					return Result{}, errors.New("diagnostic before run.begin")
				}
				var payload Diagnostic
				if err := decodeStrict(envelope.Payload, &payload); err != nil {
					return Result{}, fmt.Errorf("diagnostic: %w", err)
				}
				if payload.Message == "" || len(payload.Message) > 64<<10 || !slices.Contains([]string{"info", "warning", "error"}, payload.Severity) {
					return Result{}, errors.New("invalid diagnostic")
				}
				result.Diagnostics = append(result.Diagnostics, payload)
				if len(result.Diagnostics) > limits.MaxDiagnostics {
					return Result{}, errors.New("adapter diagnostic count exceeds limit")
				}
			case "run.end":
				if !begun || current != nil {
					return Result{}, errors.New("misplaced run.end")
				}
				var payload runEnd
				if err := decodeStrict(envelope.Payload, &payload); err != nil {
					return Result{}, fmt.Errorf("run.end: %w", err)
				}
				actual := make([]string, 0, len(units))
				for id := range units {
					actual = append(actual, id)
				}
				slices.Sort(actual)
				inventory := append([]string(nil), payload.Units...)
				slices.Sort(inventory)
				if payload.Status != "complete" || !slices.Equal(actual, inventory) || len(inventory) != len(slices.Compact(inventory)) {
					return Result{}, errors.New("run incomplete or unit inventory mismatch")
				}
				ended = true
			default:
				return Result{}, fmt.Errorf("unknown frame kind %q", envelope.Kind)
			}
		}
		if readErr != nil {
			if readErr != io.EOF && !errors.Is(readErr, os.ErrClosed) {
				return Result{}, readErr
			}
			break
		}
	}
	if !ended {
		return Result{}, errors.New("adapter stream ended before run.end")
	}
	if err := validateRunIDs(result.Units); err != nil {
		return Result{}, err
	}
	return result, nil
}

func readFrame(reader *bufio.Reader, total *int64, limits Limits) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		*total += int64(len(fragment))
		if *total > limits.MaxTotalBytes {
			return nil, errors.New("adapter stdout exceeds total byte limit")
		}
		if int64(len(line)+len(fragment)) > limits.MaxFrameBytes {
			return nil, errors.New("adapter frame exceeds byte limit")
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func validateRunIDs(units []graph.UnitFacts) error {
	seen := map[string]string{}
	check := func(kind, id string) error {
		if previous, ok := seen[id]; ok {
			return fmt.Errorf("duplicate fact id %q used by %s and %s", id, previous, kind)
		}
		seen[id] = kind
		return nil
	}
	for _, unit := range units {
		for _, document := range unit.Documents {
			if err := check("document", document.ID); err != nil {
				return err
			}
		}
		for _, symbol := range unit.Symbols {
			if err := check("symbol", symbol.ID); err != nil {
				return err
			}
		}
		for _, occurrence := range unit.Occurrences {
			if err := check("occurrence", occurrence.ID); err != nil {
				return err
			}
		}
		for _, edge := range unit.Edges {
			if err := check("edge", edge.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func factCounts(facts graph.UnitFacts) counts {
	return counts{len(facts.Documents), len(facts.Symbols), len(facts.Occurrences), len(facts.Edges)}
}

func diagnosticText(diagnostics []Diagnostic) string {
	var lines []string
	for _, diagnostic := range diagnostics {
		lines = append(lines, strings.TrimSpace(diagnostic.Message))
	}
	return strings.Join(lines, "\n")
}
