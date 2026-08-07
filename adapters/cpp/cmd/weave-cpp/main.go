// weave-cpp adapts scip-clang output to weave.adapter/v0.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/scipimport"
	"github.com/scip-code/scip/bindings/go/scip"
)

const (
	providerName       = "scip:scip-clang"
	defaultIndexer     = "scip-clang"
	maxRequestBytes    = 4 << 20
	maxToolOutputBytes = 1 << 20
	maxIndexBytes      = 256 << 20
	maxGitOutputBytes  = 16 << 20
)

var versionPattern = regexp.MustCompile(`(?m)^scip-clang ([^\s]+)\s*$`)

type options struct {
	indexer string
	compdb  string
	jobs    int
}

type producer struct {
	path    string
	version string
}

type commandResult struct {
	stdout []byte
	stderr []byte
}

type commandRunner func(context.Context, string, []string, string) (commandResult, error)

type dependencies struct {
	run commandRunner
}

func main() {
	if err := runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, dependencies{run: runCommand}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "weave-cpp: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, arguments []string, input io.Reader, output, diagnostics io.Writer, deps dependencies) error {
	configuration, operation, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	tool, probeDiagnostics, err := probe(ctx, configuration.indexer, deps.run)
	if err != nil {
		return err
	}
	if len(probeDiagnostics) != 0 {
		_, _ = diagnostics.Write(probeDiagnostics)
	}
	if operation == "describe" {
		return describe(output, tool)
	}
	request, err := readRequest(input)
	if err != nil {
		return err
	}
	if !request.Permissions.BuildTool {
		return errors.New("scip-clang requires explicit build_tool permission")
	}
	result, toolDiagnostics, err := index(ctx, request, configuration, tool, deps.run)
	if err != nil {
		return err
	}
	if len(toolDiagnostics) != 0 {
		_, _ = diagnostics.Write(toolDiagnostics)
	}
	return writeResult(output, request, tool, result)
}

func parseArguments(arguments []string) (options, string, error) {
	configuration := options{indexer: os.Getenv("WEAVE_SCIP_CLANG"), jobs: 2}
	if configuration.indexer == "" {
		configuration.indexer = defaultIndexer
	}
	if len(arguments) < 3 || arguments[len(arguments)-2] != "--protocol" || arguments[len(arguments)-1] != adapter.Protocol {
		return options{}, "", errors.New("usage: weave-cpp [--scip-clang=PATH] [--compdb=PATH] [--jobs=N] (describe|index) --protocol weave.adapter/v0")
	}
	operation := arguments[len(arguments)-3]
	if operation != "describe" && operation != "index" {
		return options{}, "", errors.New("operation must be describe or index")
	}
	seen := map[string]bool{}
	for _, value := range arguments[:len(arguments)-3] {
		name, argument, ok := strings.Cut(value, "=")
		if !ok || argument == "" || !slices.Contains([]string{"--scip-clang", "--compdb", "--jobs"}, name) {
			return options{}, "", fmt.Errorf("unsupported adapter argument %q", value)
		}
		if seen[name] {
			return options{}, "", fmt.Errorf("duplicate adapter argument %s", name)
		}
		seen[name] = true
		switch name {
		case "--scip-clang":
			configuration.indexer = argument
		case "--compdb":
			configuration.compdb = argument
		case "--jobs":
			jobs, err := strconv.Atoi(argument)
			if err != nil || jobs < 1 || jobs > 256 {
				return options{}, "", errors.New("--jobs must be between 1 and 256")
			}
			configuration.jobs = jobs
		}
	}
	return configuration, operation, nil
}

func probe(ctx context.Context, indexer string, run commandRunner) (producer, []byte, error) {
	path, err := exec.LookPath(indexer)
	if err != nil {
		return producer{}, nil, fmt.Errorf("find scip-clang executable %q: %w", indexer, err)
	}
	if absolute, absoluteErr := filepath.Abs(path); absoluteErr == nil {
		path = absolute
	}
	result, err := run(ctx, path, []string{"--version"}, "")
	if err != nil {
		return producer{}, result.stderr, fmt.Errorf("probe scip-clang: %w%s", err, stderrSuffix(result.stderr))
	}
	match := versionPattern.FindSubmatch(result.stdout)
	if len(match) != 2 {
		return producer{}, result.stderr, fmt.Errorf("scip-clang --version returned an unrecognized version: %q", strings.TrimSpace(string(result.stdout)))
	}
	return producer{path: path, version: string(match[1])}, result.stderr, nil
}

func describe(output io.Writer, tool producer) error {
	return json.NewEncoder(output).Encode(map[string]any{
		"protocols":          []string{adapter.Protocol},
		"provider":           map[string]string{"name": providerName, "version": tool.version},
		"languages":          []string{"c", "cpp", "cuda"},
		"operations":         []string{"index"},
		"refresh_modes":      []string{"full"},
		"fact_encoding":      adapter.FactEncoding,
		"position_encodings": []string{"utf8-byte"},
		"requires": map[string]any{
			"executables":        []string{"scip-clang", "git"},
			"may_run_build_tool": true,
		},
	})
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

func index(ctx context.Context, request adapter.IndexRequest, configuration options, tool producer, run commandRunner) (scipimport.Result, []byte, error) {
	root, err := canonicalDirectory(request.RepositoryRoot)
	if err != nil {
		return scipimport.Result{}, nil, err
	}
	compdb, err := selectCompilationDatabase(ctx, root, configuration.compdb)
	if err != nil {
		return scipimport.Result{}, nil, err
	}
	temporary, err := os.MkdirTemp("", "weave-cpp-")
	if err != nil {
		return scipimport.Result{}, nil, fmt.Errorf("create private adapter directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	work := filepath.Join(temporary, "work")
	supplementary := filepath.Join(temporary, "supplementary")
	for _, directory := range []string{work, supplementary} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return scipimport.Result{}, nil, fmt.Errorf("create private scip-clang directory: %w", err)
		}
	}
	indexPath := filepath.Join(temporary, "index.scip")
	arguments := []string{
		"--compdb-path=" + compdb,
		"--index-output-path=" + indexPath,
		"--temporary-output-dir=" + work,
		"--supplementary-output-dir=" + supplementary,
		"--jobs=" + strconv.Itoa(configuration.jobs),
		"--no-progress-report",
		"--log-level=warning",
	}
	command, err := run(ctx, tool.path, arguments, root)
	if err != nil {
		return scipimport.Result{}, command.stderr, fmt.Errorf("run scip-clang: %w%s", err, stderrSuffix(command.stderr))
	}
	if len(bytes.TrimSpace(command.stdout)) != 0 {
		return scipimport.Result{}, command.stderr, errors.New("scip-clang wrote unexpected stdout")
	}
	result, err := (scipimport.Importer{Limits: scipimport.Limits{
		MaxIndexBytes: maxIndexBytes,
		MaxFacts:      request.Limits.MaxFacts,
	}}).ImportFile(ctx, indexPath, scipimport.Options{
		RepositoryRoot:         root,
		RepositoryIdentity:     request.RepositoryIdentity,
		LegacyPositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
	})
	if err != nil {
		return scipimport.Result{}, command.stderr, fmt.Errorf("import scip-clang index: %w", err)
	}
	if result.Provider != providerName || result.ProviderVersion != tool.version {
		return scipimport.Result{}, command.stderr, fmt.Errorf("SCIP producer is %s %s, want %s %s", result.Provider, result.ProviderVersion, providerName, tool.version)
	}
	return result, command.stderr, nil
}

func canonicalDirectory(value string) (string, error) {
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make repository root absolute: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository_root must resolve to a directory")
	}
	return filepath.Clean(resolved), nil
}

func selectCompilationDatabase(ctx context.Context, root, explicit string) (string, error) {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return "", err
	}
	root = canonicalRoot
	if explicit != "" {
		candidate := explicit
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		return validateCompilationDatabase(root, candidate)
	}
	var matches []string
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "-C", root, "ls-files", "-co", "--exclude-standard", "-z", "--")
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = maxGitOutputBytes, 64<<10
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if stdout.err != nil && err == nil {
		err = stdout.err
	}
	if stderr.err != nil && err == nil {
		err = stderr.err
	}
	if err != nil {
		return "", fmt.Errorf("list Git-visible compilation databases: %w%s", err, stderrSuffix(stderr.Bytes()))
	}
	for _, raw := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		if !utf8.Valid(raw) {
			return "", errors.New("Git-visible path is not UTF-8")
		}
		relative := string(raw)
		if filepath.Base(filepath.FromSlash(relative)) != "compile_commands.json" {
			continue
		}
		if !fs.ValidPath(relative) || strings.Contains(relative, `\`) {
			return "", fmt.Errorf("Git returned unsafe compilation database path %q", relative)
		}
		validated, err := validateCompilationDatabase(root, filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		matches = append(matches, validated)
	}
	slices.Sort(matches)
	matches = slices.Compact(matches)
	if len(matches) == 0 {
		return "", errors.New("no Git-visible compile_commands.json found; pass an ignored/generated database explicitly with --compdb")
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("multiple compilation databases found; select one with --compdb: %s", strings.Join(matches, ", "))
	}
	return matches[0], nil
}

func validateCompilationDatabase(root, candidate string) (string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve compilation database path: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("compilation database must be inside repository")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect compilation database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("compilation database may not be a symbolic link")
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return "", errors.New("compilation database must be a regular file no larger than 64 MiB")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve compilation database: %w", err)
	}
	relative, err = filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("compilation database escapes repository")
	}
	return filepath.Clean(resolved), nil
}

func runCommand(ctx context.Context, path string, arguments []string, directory string) (commandResult, error) {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Dir = directory
	command.WaitDelay = 2 * time.Second
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = maxToolOutputBytes, maxToolOutputBytes
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
	return commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}, err
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	if buffer.Len()+len(value) > buffer.limit {
		buffer.err = fmt.Errorf("tool output exceeds %d bytes", buffer.limit)
		return 0, buffer.err
	}
	return buffer.Buffer.Write(value)
}

func stderrSuffix(value []byte) string {
	text := strings.TrimSpace(string(value))
	if text == "" {
		return ""
	}
	return ": stderr: " + text
}

type frameWriter struct {
	output    io.Writer
	requestID string
	limits    adapter.RequestLimits
	frames    int
	total     int64
}

func writeResult(output io.Writer, request adapter.IndexRequest, tool producer, result scipimport.Result) error {
	writer := &frameWriter{output: output, requestID: request.RequestID, limits: request.Limits}
	if err := writer.emit("run.begin", map[string]any{
		"provider":      map[string]string{"name": providerName, "version": tool.version},
		"fact_encoding": adapter.FactEncoding,
	}); err != nil {
		return err
	}
	unitIDs := make([]string, 0, len(result.Units))
	for _, facts := range result.Units {
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
