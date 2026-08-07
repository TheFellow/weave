// Package adapter implements Weave's versioned one-shot native adapter boundary.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
)

const (
	Protocol     = "weave.adapter/v0"
	FactEncoding = "weave.facts/v0"
)

// Limits bound all data accepted from an adapter process.
type Limits struct {
	MaxFrameBytes  int64
	MaxTotalBytes  int64
	MaxFrames      int
	MaxFacts       int
	MaxDiagnostics int
	MaxStderrBytes int64
	WaitDelay      time.Duration
}

func (limits Limits) withDefaults() Limits {
	if limits.MaxFrameBytes <= 0 {
		limits.MaxFrameBytes = 1 << 20
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = 32 << 20
	}
	if limits.MaxFrames <= 0 {
		limits.MaxFrames = 100_000
	}
	if limits.MaxFacts <= 0 {
		limits.MaxFacts = 1_000_000
	}
	if limits.MaxDiagnostics <= 0 {
		limits.MaxDiagnostics = 1_000
	}
	if limits.MaxStderrBytes <= 0 {
		limits.MaxStderrBytes = 256 << 10
	}
	if limits.WaitDelay <= 0 {
		limits.WaitDelay = 2 * time.Second
	}
	return limits
}

// Executable describes one trusted executable location and literal arguments.
type Executable struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// Provider identifies the adapter implementation.
type Provider struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities is the bounded describe response.
type Capabilities struct {
	Protocols        []string     `json:"protocols"`
	Provider         Provider     `json:"provider"`
	Languages        []string     `json:"languages"`
	Operations       []string     `json:"operations"`
	RefreshModes     []string     `json:"refresh_modes"`
	FactEncoding     string       `json:"fact_encoding"`
	PositionEncoding []string     `json:"position_encodings"`
	Requires         Requirements `json:"requires"`
}

// Requirements disclose external runtime behavior during capability discovery.
type Requirements struct {
	Executables     []string `json:"executables"`
	MayRunBuildTool bool     `json:"may_run_build_tool"`
}

// Permissions are denied unless explicitly enabled by the caller.
type Permissions struct {
	Network       bool `json:"network"`
	Restore       bool `json:"restore"`
	BuildTool     bool `json:"build_tool"`
	RunGenerators bool `json:"run_generators"`
}

// IndexRequest is the single request sent to an adapter.
type IndexRequest struct {
	Protocol           string            `json:"protocol"`
	RequestID          string            `json:"request_id"`
	RepositoryRoot     string            `json:"repository_root"`
	RepositoryIdentity string            `json:"repository_identity,omitempty"`
	Variant            string            `json:"variant,omitempty"`
	ChangedPaths       []string          `json:"changed_paths,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
	Permissions        Permissions       `json:"permissions"`
	Limits             RequestLimits     `json:"limits"`
}

type RequestLimits struct {
	MaxFrameBytes int64 `json:"max_frame_bytes"`
	MaxTotalBytes int64 `json:"max_total_bytes"`
	MaxFrames     int   `json:"max_frames"`
	MaxFacts      int   `json:"max_facts"`
}

// Diagnostic is structured adapter output safe to render on stderr.
type Diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	UnitID   string `json:"unit_id,omitempty"`
}

// Result is returned only after the complete run validates.
type Result struct {
	Provider    Provider
	Units       []graph.UnitFacts
	Diagnostics []Diagnostic
	Stderr      string
}

// Runner executes and validates one-shot adapters.
type Runner struct{ Limits Limits }

// Describe negotiates the experimental protocol without running indexing.
func (runner Runner) Describe(ctx context.Context, executable Executable) (Capabilities, string, error) {
	limits := runner.Limits.withDefaults()
	stdout, stderr, err := runBounded(ctx, executable, append(append([]string{}, executable.Args...), "describe", "--protocol", Protocol), nil, limits)
	if err != nil {
		return Capabilities{}, stderr, fmt.Errorf("describe adapter: %w%s", err, stderrSuffix(stderr))
	}
	var capabilities Capabilities
	if err := decodeStrict(stdout, &capabilities); err != nil {
		return Capabilities{}, stderr, fmt.Errorf("decode adapter capabilities: %w", err)
	}
	if !slices.Contains(capabilities.Protocols, Protocol) {
		return Capabilities{}, stderr, fmt.Errorf("adapter does not support %s", Protocol)
	}
	if capabilities.Provider.Name == "" || capabilities.Provider.Version == "" {
		return Capabilities{}, stderr, errors.New("adapter provider name and version are required")
	}
	if !slices.Contains(capabilities.Operations, "index") || capabilities.FactEncoding != FactEncoding {
		return Capabilities{}, stderr, fmt.Errorf("adapter does not support index with %s", FactEncoding)
	}
	return capabilities, stderr, nil
}

// Index describes, runs, and fully validates one adapter refresh.
func (runner Runner) Index(ctx context.Context, executable Executable, request IndexRequest) (Result, error) {
	limits := runner.Limits.withDefaults()
	if request.RequestID == "" || request.RepositoryRoot == "" {
		return Result{}, errors.New("request ID and repository root are required")
	}
	request.Protocol = Protocol
	request.Limits = RequestLimits{limits.MaxFrameBytes, limits.MaxTotalBytes, limits.MaxFrames, limits.MaxFacts}
	capabilities, describeStderr, err := runner.Describe(ctx, executable)
	if err != nil {
		return Result{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Result{}, fmt.Errorf("encode adapter request: %w", err)
	}
	encoded = append(encoded, '\n')

	result, stderr, err := runIndex(ctx, executable, request, capabilities, encoded, limits)
	result.Stderr = joinDiagnostics(describeStderr, stderr)
	if err != nil {
		return Result{}, fmt.Errorf("index with adapter %s: %w%s", capabilities.Provider.Name, err, stderrSuffix(result.Stderr))
	}
	return result, nil
}

func runIndex(ctx context.Context, executable Executable, request IndexRequest, capabilities Capabilities, input []byte, limits Limits) (Result, string, error) {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := append(append([]string{}, executable.Args...), "index", "--protocol", Protocol)
	command := exec.CommandContext(childCtx, executable.Path, args...)
	command.Dir, command.Env, command.WaitDelay = executable.Dir, executable.Env, limits.WaitDelay
	command.Stdin = bytes.NewReader(input)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Result{}, "", err
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return Result{}, "", err
	}
	if err := command.Start(); err != nil {
		return Result{}, "", err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()

	var wg sync.WaitGroup
	var result Result
	var parseErr, stderrErr error
	var stderr []byte
	wg.Add(2)
	go func() {
		defer wg.Done()
		result, parseErr = parseFrames(stdout, request.RequestID, capabilities, limits)
		if parseErr != nil {
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		stderr, stderrErr = readBounded(stderrPipe, limits.MaxStderrBytes)
		if stderrErr != nil {
			cancel()
		}
	}()
	wg.Wait()
	waitErr := <-wait
	stderrText := string(stderr)
	if ctx.Err() != nil {
		return Result{}, stderrText, ctx.Err()
	}
	if stderrErr != nil {
		return Result{}, stderrText, fmt.Errorf("read stderr: %w", stderrErr)
	}
	if waitErr != nil && parseErr != nil && strings.Contains(parseErr.Error(), "before run.end") {
		return Result{}, stderrText, waitErr
	}
	if parseErr != nil {
		return Result{}, stderrText, parseErr
	}
	if waitErr != nil {
		return Result{}, stderrText, waitErr
	}
	return result, stderrText, nil
}

func runBounded(ctx context.Context, executable Executable, args []string, stdin []byte, limits Limits) ([]byte, string, error) {
	command := exec.CommandContext(ctx, executable.Path, args...)
	command.Dir, command.Env, command.WaitDelay = executable.Dir, executable.Env, limits.WaitDelay
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr limitWriter
	stdout.limit, stderr.limit = limits.MaxFrameBytes, limits.MaxStderrBytes
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if stdout.err != nil && err == nil {
		err = stdout.err
	}
	if stderr.err != nil && err == nil {
		err = stderr.err
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return stdout.Bytes(), stderr.String(), err
}

type limitWriter struct {
	bytes.Buffer
	limit int64
	err   error
}

func (writer *limitWriter) Write(value []byte) (int, error) {
	remaining := writer.limit - int64(writer.Len())
	if remaining <= 0 || int64(len(value)) > remaining {
		writer.err = errors.New("output exceeds configured byte limit")
		return 0, writer.err
	}
	return writer.Buffer.Write(value)
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	limited := io.LimitReader(reader, maximum+1)
	value, err := io.ReadAll(limited)
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return value, err
	}
	if int64(len(value)) > maximum {
		return value[:maximum], errors.New("output exceeds configured byte limit")
	}
	if !utf8.Valid(value) {
		return nil, errors.New("output is not valid UTF-8")
	}
	return value, nil
}

func decodeStrict(value []byte, target any) error {
	if len(bytes.TrimSpace(value)) == 0 || !utf8.Valid(value) {
		return errors.New("expected one UTF-8 JSON object")
	}
	if err := validateJSONDepth(value, 100); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateJSONDepth(value []byte, maximum int) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > maximum {
				return fmt.Errorf("JSON nesting exceeds depth limit %d", maximum)
			}
		case '}', ']':
			depth--
		}
	}
}

func stderrSuffix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return ": stderr: " + value
}

func joinDiagnostics(values ...string) string {
	var nonempty []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			nonempty = append(nonempty, value)
		}
	}
	return strings.Join(nonempty, "\n")
}
