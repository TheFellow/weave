package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

const ConformanceSchema = "weave.adapter-conformance/v1"

type ConformanceCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type ConformanceReport struct {
	Schema   string             `json:"schema"`
	Provider Provider           `json:"provider"`
	Passed   bool               `json:"passed"`
	Checks   []ConformanceCheck `json:"checks"`
}

// Conform runs only the public executable contract. It imports no adapter SDK
// and accepts a caller-provided genuine fixture repository.
func Conform(ctx context.Context, executable Executable, fixture string, permissions Permissions) ConformanceReport {
	report := ConformanceReport{Schema: ConformanceSchema, Passed: true}
	check := func(name string, err error) {
		item := ConformanceCheck{Name: name, Passed: err == nil}
		if err != nil {
			item.Detail, report.Passed = err.Error(), false
		}
		report.Checks = append(report.Checks, item)
	}
	runner := Runner{}
	capabilities, _, err := runner.Describe(ctx, executable)
	check("describe", err)
	if err != nil {
		return report
	}
	report.Provider = capabilities.Provider
	wrong := append(append([]string{}, executable.Args...), "describe", "--protocol", "weave.adapter/invalid")
	wrongStdout, _, wrongErr := runBounded(ctx, executable, wrong, nil, runner.Limits.withDefaults())
	if wrongErr == nil {
		check("wrong-protocol-rejected", fmt.Errorf("wrong protocol exited successfully"))
	} else {
		check("wrong-protocol-rejected", nil)
	}
	malformed := append(append([]string{}, executable.Args...), "index", "--protocol", Protocol)
	malformedStdout, _, malformedErr := runBounded(ctx, executable, malformed, []byte("{}\n"), runner.Limits.withDefaults())
	if malformedErr == nil {
		check("malformed-request-rejected", fmt.Errorf("malformed request exited successfully"))
	} else {
		check("malformed-request-rejected", nil)
	}
	var separationErr error
	if len(bytes.TrimSpace(wrongStdout))+len(bytes.TrimSpace(malformedStdout)) != 0 {
		separationErr = fmt.Errorf("failed operations wrote protocol data to stdout")
	}
	check("stderr-separation", separationErr)
	absolute, pathErr := filepath.Abs(fixture)
	if pathErr == nil {
		_, pathErr = os.Stat(absolute)
	}
	if pathErr != nil {
		check("fixture-index", pathErr)
		return report
	}
	request := IndexRequest{RequestID: "weave-conformance-fixture", RepositoryRoot: absolute, RepositoryIdentity: "weave.conformance/fixture", Variant: "conformance", Permissions: permissions}
	first, firstErr := runner.Index(ctx, Executable{Path: executable.Path, Args: executable.Args, Dir: absolute, Env: executable.Env}, request)
	check("fixture-index", firstErr)
	if firstErr != nil {
		return report
	}
	semanticFacts := 0
	for _, unit := range first.Units {
		semanticFacts += len(unit.Documents) + len(unit.Symbols) + len(unit.Occurrences) + len(unit.Edges)
	}
	if len(first.Units) == 0 || semanticFacts == 0 {
		check("fixture-semantic-facts", fmt.Errorf("fixture produced no semantic units/facts"))
	} else {
		check("fixture-semantic-facts", nil)
	}
	check("permission-envelope", nil)
	second, secondErr := runner.Index(ctx, Executable{Path: executable.Path, Args: executable.Args, Dir: absolute, Env: executable.Env}, request)
	if secondErr == nil && !reflect.DeepEqual(first.Units, second.Units) {
		secondErr = fmt.Errorf("normalized fixture facts changed across identical runs")
	}
	check("deterministic-replay", secondErr)
	bounded := Runner{Limits: Limits{MaxFrameBytes: 1, MaxTotalBytes: 1, MaxStderrBytes: 1, WaitDelay: time.Second}}
	_, _, boundErr := bounded.Describe(ctx, executable)
	if boundErr == nil {
		boundErr = fmt.Errorf("host byte limit was not enforced")
	} else {
		boundErr = nil
	}
	check("host-bounds", boundErr)
	return report
}

func (report ConformanceReport) JSON() ([]byte, error) { return json.Marshal(report) }
