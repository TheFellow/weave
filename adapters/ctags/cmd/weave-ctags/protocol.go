package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/graph"
)

const maxRequestBytes = 4 << 20

func jsonEncode(output io.Writer, value any) error {
	return json.NewEncoder(output).Encode(value)
}

func readRequest(input io.Reader) (adapter.IndexRequest, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		return adapter.IndexRequest{}, fmt.Errorf("read index request: %w", err)
	}
	if len(data) == 0 || len(data) > maxRequestBytes {
		return adapter.IndexRequest{}, errors.New("exactly one bounded JSON request is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request adapter.IndexRequest
	if err := decoder.Decode(&request); err != nil {
		return adapter.IndexRequest{}, fmt.Errorf("decode index request: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return adapter.IndexRequest{}, errors.New("index request must contain exactly one JSON value")
	}
	if request.Protocol != adapter.Protocol || request.RequestID == "" {
		return adapter.IndexRequest{}, errors.New("unsupported protocol or empty request_id")
	}
	if !filepath.IsAbs(request.RepositoryRoot) {
		return adapter.IndexRequest{}, errors.New("repository_root must be absolute")
	}
	if request.Limits.MaxFrameBytes <= 0 || request.Limits.MaxTotalBytes <= 0 || request.Limits.MaxFrames <= 0 || request.Limits.MaxFacts <= 0 {
		return adapter.IndexRequest{}, errors.New("request limits must be positive")
	}
	return request, nil
}

type frameWriter struct {
	output    io.Writer
	requestID string
	limits    adapter.RequestLimits
	frames    int
	total     int64
}

func writeResult(output io.Writer, request adapter.IndexRequest, tool producer, result indexResult) error {
	writer := &frameWriter{output: output, requestID: request.RequestID, limits: request.Limits}
	if err := writer.emit("run.begin", map[string]any{
		"provider":      map[string]string{"name": providerName, "version": tool.version},
		"fact_encoding": adapter.FactEncoding,
	}); err != nil {
		return err
	}
	for _, diagnostic := range result.diagnostics {
		if err := writer.emit("diagnostic", diagnostic); err != nil {
			return err
		}
	}
	unitIDs := make([]string, 0, len(result.units))
	for _, facts := range result.units {
		unitIDs = append(unitIDs, facts.Unit.ID)
		if err := writer.unit(facts); err != nil {
			return err
		}
	}
	slices.Sort(unitIDs)
	return writer.emit("run.end", map[string]any{"status": "complete", "units": unitIDs})
}

func (writer *frameWriter) unit(facts graph.UnitFacts) error {
	if err := writer.emit("unit.begin", map[string]any{"unit": facts.Unit}); err != nil {
		return err
	}
	if err := emitFactBatches(writer, "documents", facts.Documents); err != nil {
		return err
	}
	if err := emitFactBatches(writer, "symbols", facts.Symbols); err != nil {
		return err
	}
	if err := emitFactBatches(writer, "occurrences", facts.Occurrences); err != nil {
		return err
	}
	if err := emitFactBatches(writer, "edges", facts.Edges); err != nil {
		return err
	}
	return writer.emit("unit.end", map[string]any{
		"status": "complete",
		"counts": map[string]int{
			"documents": len(facts.Documents), "symbols": len(facts.Symbols),
			"occurrences": len(facts.Occurrences), "edges": len(facts.Edges),
		},
	})
}

func emitFactBatches[T any](writer *frameWriter, name string, facts []T) error {
	batch := make([]T, 0, min(len(facts), 256))
	for _, fact := range facts {
		if len(batch) == 256 {
			if err := writer.emit("facts", map[string]any{name: batch}); err != nil {
				return err
			}
			batch = make([]T, 0, 256)
		}
		candidate := append(append([]T(nil), batch...), fact)
		if writer.encodedSize("facts", map[string]any{name: candidate}) > writer.limits.MaxFrameBytes {
			if len(batch) == 0 {
				return fmt.Errorf("one %s fact exceeds max_frame_bytes", name)
			}
			if err := writer.emit("facts", map[string]any{name: batch}); err != nil {
				return err
			}
			batch = []T{fact}
			if writer.encodedSize("facts", map[string]any{name: batch}) > writer.limits.MaxFrameBytes {
				return fmt.Errorf("one %s fact exceeds max_frame_bytes", name)
			}
		} else {
			batch = candidate
		}
	}
	if len(batch) != 0 {
		return writer.emit("facts", map[string]any{name: batch})
	}
	return nil
}

func (writer *frameWriter) encodedSize(kind string, payload any) int64 {
	value, err := writer.encode(kind, payload)
	if err != nil {
		return writer.limits.MaxFrameBytes + 1
	}
	return int64(len(value))
}

func (writer *frameWriter) encode(kind string, payload any) ([]byte, error) {
	value, err := json.Marshal(map[string]any{
		"protocol": adapter.Protocol, "request_id": writer.requestID,
		"kind": kind, "payload": payload,
	})
	if err != nil {
		return nil, err
	}
	return append(value, '\n'), nil
}

func (writer *frameWriter) emit(kind string, payload any) error {
	value, err := writer.encode(kind, payload)
	if err != nil {
		return err
	}
	if int64(len(value)) > writer.limits.MaxFrameBytes {
		return fmt.Errorf("%s frame exceeds max_frame_bytes", kind)
	}
	if writer.frames+1 > writer.limits.MaxFrames {
		return errors.New("adapter response exceeds max_frames")
	}
	if writer.total+int64(len(value)) > writer.limits.MaxTotalBytes {
		return errors.New("adapter response exceeds max_total_bytes")
	}
	if _, err := writer.output.Write(value); err != nil {
		return err
	}
	writer.frames++
	writer.total += int64(len(value))
	return nil
}
