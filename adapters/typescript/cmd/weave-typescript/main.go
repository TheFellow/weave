// weave-typescript adapts scip-typescript output to weave.adapter/v0.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/scipimport"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

const (
	providerName       = "scip:scip-typescript"
	defaultIndexer     = "scip-typescript"
	supportedVersion   = "0.4.0"
	maxRequestBytes    = 4 << 20
	maxToolOutputBytes = 1 << 20
	maxIndexBytes      = 256 << 20
)

type options struct {
	indexer string
	project string
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
		_, _ = fmt.Fprintf(os.Stderr, "weave-typescript: %v\n", err)
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
	result, toolDiagnostics, err := index(ctx, request, configuration, tool, deps.run)
	if len(toolDiagnostics) != 0 {
		_, _ = diagnostics.Write(toolDiagnostics)
	}
	if err != nil {
		return err
	}
	return writeResult(output, request, tool, result)
}

func parseArguments(arguments []string) (options, string, error) {
	configuration := options{indexer: os.Getenv("WEAVE_SCIP_TYPESCRIPT")}
	if configuration.indexer == "" {
		configuration.indexer = defaultIndexer
	}
	if len(arguments) < 3 || arguments[len(arguments)-2] != "--protocol" || arguments[len(arguments)-1] != adapter.Protocol {
		return options{}, "", errors.New("usage: weave-typescript [--scip-typescript=PATH] [--project=PATH] (describe|index) --protocol weave.adapter/v0")
	}
	operation := arguments[len(arguments)-3]
	if operation != "describe" && operation != "index" {
		return options{}, "", errors.New("operation must be describe or index")
	}
	seen := map[string]bool{}
	for _, value := range arguments[:len(arguments)-3] {
		name, argument, ok := strings.Cut(value, "=")
		if !ok || argument == "" || !slices.Contains([]string{"--scip-typescript", "--project"}, name) {
			return options{}, "", fmt.Errorf("unsupported adapter argument %q", value)
		}
		if seen[name] {
			return options{}, "", fmt.Errorf("duplicate adapter argument %s", name)
		}
		seen[name] = true
		switch name {
		case "--scip-typescript":
			configuration.indexer = argument
		case "--project":
			configuration.project = argument
		}
	}
	return configuration, operation, nil
}

func probe(ctx context.Context, indexer string, run commandRunner) (producer, []byte, error) {
	executable, err := exec.LookPath(indexer)
	if err != nil {
		return producer{}, nil, fmt.Errorf("find scip-typescript executable %q: %w", indexer, err)
	}
	if absolute, absoluteErr := filepath.Abs(executable); absoluteErr == nil {
		executable = absolute
	}
	result, err := run(ctx, executable, []string{"--version"}, "")
	if err != nil {
		return producer{}, result.stderr, fmt.Errorf("probe scip-typescript: %w%s", err, stderrSuffix(result.stderr))
	}
	version := strings.TrimSpace(string(result.stdout))
	if version != supportedVersion {
		return producer{}, result.stderr, fmt.Errorf("unsupported scip-typescript version %q; want %s", version, supportedVersion)
	}
	return producer{path: executable, version: version}, result.stderr, nil
}

func describe(output io.Writer, tool producer) error {
	return json.NewEncoder(output).Encode(map[string]any{
		"protocols":          []string{adapter.Protocol},
		"provider":           map[string]string{"name": providerName, "version": tool.version},
		"languages":          []string{"javascript", "javascriptreact", "typescript", "typescriptreact"},
		"operations":         []string{"index"},
		"refresh_modes":      []string{"full"},
		"fact_encoding":      adapter.FactEncoding,
		"position_encodings": []string{"utf8-byte"},
		"requires": map[string]any{
			"executables":        []string{"node", "scip-typescript"},
			"may_run_build_tool": false,
		},
		"claims": map[string]any{
			"inputs": map[string]any{
				"extensions":      []string{".js", ".jsx", ".ts", ".tsx"},
				"filenames":       []string{"jsconfig.json", "package.json", "tsconfig.json"},
				"project_markers": []string{"jsconfig.json", "tsconfig.json"},
			},
			"evidence": []string{"exact"}, "invalidation_all_files": true,
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
	project, err := selectProject(root, configuration.project)
	if err != nil {
		return scipimport.Result{}, nil, err
	}
	temporary, err := os.MkdirTemp("", "weave-typescript-")
	if err != nil {
		return scipimport.Result{}, nil, fmt.Errorf("create private adapter directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	indexPath := filepath.Join(temporary, "index.scip")
	arguments := []string{
		"index", "--cwd", root, "--output", indexPath,
		"--no-progress-bar", "--no-global-caches", project,
	}
	command, err := run(ctx, tool.path, arguments, root)
	diagnostics := toolDiagnostics(command)
	if err != nil {
		return scipimport.Result{}, diagnostics, fmt.Errorf("run scip-typescript: %w%s", err, stderrSuffix(command.stderr))
	}
	encoded, err := readIndex(indexPath)
	if err != nil {
		return scipimport.Result{}, diagnostics, err
	}
	encoded, err = prepareIndex(encoded, runtime.GOOS == "windows")
	if err != nil {
		return scipimport.Result{}, diagnostics, fmt.Errorf("validate scip-typescript index: %w", err)
	}
	result, err := (scipimport.Importer{Limits: scipimport.Limits{
		MaxIndexBytes: maxIndexBytes,
		MaxFacts:      request.Limits.MaxFacts,
	}}).Import(ctx, encoded, scipimport.Options{
		RepositoryRoot:         root,
		RepositoryIdentity:     request.RepositoryIdentity,
		LegacyPositionEncoding: scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart,
	})
	if err != nil {
		return scipimport.Result{}, diagnostics, fmt.Errorf("import scip-typescript index: %w", err)
	}
	if result.Provider != providerName || result.ProviderVersion != tool.version {
		return scipimport.Result{}, diagnostics, fmt.Errorf("SCIP producer is %s %s, want %s %s", result.Provider, result.ProviderVersion, providerName, tool.version)
	}
	return result, diagnostics, nil
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

func selectProject(root, explicit string) (string, error) {
	if explicit != "" {
		candidate := explicit
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		return validateProject(root, candidate)
	}
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		candidate := filepath.Join(root, name)
		if _, err := os.Lstat(candidate); err == nil {
			return validateProject(root, candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect %s: %w", name, err)
		}
	}
	return "", errors.New("no root tsconfig.json or jsconfig.json found; add one or select a repository-contained project with --project (upstream --infer-tsconfig is intentionally disabled because it writes into the repository)")
}

func validateProject(root, candidate string) (string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("project path must be inside repository")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect project path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("project path may not be a symbolic link")
	}
	if !info.IsDir() && (!info.Mode().IsRegular() || info.Size() > 16<<20 || strings.ToLower(filepath.Ext(absolute)) != ".json") {
		return "", errors.New("project path must be a directory or regular JSON file no larger than 16 MiB")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	relative, err = filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("project path escapes repository")
	}
	return filepath.Clean(resolved), nil
}

func readIndex(indexPath string) ([]byte, error) {
	info, err := os.Lstat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("inspect scip-typescript index: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxIndexBytes {
		return nil, fmt.Errorf("scip-typescript index must be a regular file no larger than %d bytes", maxIndexBytes)
	}
	encoded, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("read scip-typescript index: %w", err)
	}
	return encoded, nil
}

func prepareIndex(encoded []byte, windows bool) ([]byte, error) {
	var index scip.Index
	if err := (proto.UnmarshalOptions{RecursionLimit: 100}).Unmarshal(encoded, &index); err != nil {
		return nil, fmt.Errorf("decode SCIP protobuf: %w", err)
	}
	if index.Metadata == nil || index.Metadata.ToolInfo == nil || index.Metadata.ToolInfo.Name != "scip-typescript" || index.Metadata.ToolInfo.Version != supportedVersion {
		return nil, errors.New("SCIP metadata must identify scip-typescript 0.4.0")
	}
	for number, document := range index.Documents {
		if document == nil {
			return nil, fmt.Errorf("document %d is nil", number)
		}
		if windows {
			document.RelativePath = strings.ReplaceAll(document.RelativePath, `\`, "/")
		}
		if document.Language == "" {
			document.Language = sourceLanguage(document.RelativePath)
		}
	}
	result, err := proto.Marshal(&index)
	if err != nil {
		return nil, fmt.Errorf("encode normalized SCIP protobuf: %w", err)
	}
	if len(result) > maxIndexBytes {
		return nil, errors.New("normalized SCIP index exceeds byte limit")
	}
	return result, nil
}

func sourceLanguage(name string) string {
	lower := strings.ToLower(name)
	switch path.Ext(lower) {
	case ".tsx":
		return "typescriptreact"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".jsx":
		return "javascriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	default:
		return ""
	}
}

func toolDiagnostics(result commandResult) []byte {
	var diagnostics bytes.Buffer
	for _, stream := range [][]byte{result.stdout, result.stderr} {
		if len(stream) == 0 {
			continue
		}
		_, _ = diagnostics.Write(stream)
		if stream[len(stream)-1] != '\n' {
			_ = diagnostics.WriteByte('\n')
		}
	}
	return diagnostics.Bytes()
}

func runCommand(ctx context.Context, executable string, arguments []string, directory string) (commandResult, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	command.WaitDelay = 2 * time.Second
	command.Env = append(os.Environ(),
		"npm_config_offline=true", "npm_config_audit=false", "npm_config_fund=false",
		"YARN_ENABLE_NETWORK=false",
	)
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
