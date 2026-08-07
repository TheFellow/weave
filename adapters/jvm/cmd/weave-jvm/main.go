// weave-jvm adapts scip-java output to weave.adapter/v0.
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
	"path/filepath"
	"regexp"
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
	providerName           = "scip:scip-java"
	defaultIndexer         = "scip-java"
	defaultVersion         = "0.13.1"
	defaultMetadataVersion = "0.0.0-SNAPSHOT"
	maxRequestBytes        = 4 << 20
	maxToolOutputBytes     = 1 << 20
	maxIndexBytes          = 256 << 20
)

var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,127}$`)

type options struct {
	indexer         string
	version         string
	metadataVersion string
	buildTool       string
}

type producer struct {
	path            string
	version         string
	metadataVersion string
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
		_, _ = fmt.Fprintf(os.Stderr, "weave-jvm: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, arguments []string, input io.Reader, output, diagnostics io.Writer, deps dependencies) error {
	configuration, operation, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if operation == "describe" {
		return describe(output, configuration)
	}
	request, err := readRequest(input)
	if err != nil {
		return err
	}
	if err := requirePermissions(request.Permissions); err != nil {
		return err
	}
	tool, err := resolveProducer(configuration)
	if err != nil {
		return err
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
	configuration := options{
		indexer:         os.Getenv("WEAVE_SCIP_JAVA"),
		version:         os.Getenv("WEAVE_SCIP_JAVA_VERSION"),
		metadataVersion: os.Getenv("WEAVE_SCIP_JAVA_METADATA_VERSION"),
	}
	if configuration.indexer == "" {
		configuration.indexer = defaultIndexer
	}
	if configuration.version == "" {
		configuration.version = defaultVersion
	}
	if configuration.metadataVersion == "" {
		configuration.metadataVersion = defaultMetadataVersion
	}
	if len(arguments) < 3 || arguments[len(arguments)-2] != "--protocol" || arguments[len(arguments)-1] != adapter.Protocol {
		return options{}, "", errors.New("usage: weave-jvm [--scip-java=PATH] [--producer-version=VERSION] [--metadata-version=VERSION] [--build-tool=TOOL] (describe|index) --protocol weave.adapter/v0")
	}
	operation := arguments[len(arguments)-3]
	if operation != "describe" && operation != "index" {
		return options{}, "", errors.New("operation must be describe or index")
	}
	seen := map[string]bool{}
	for _, value := range arguments[:len(arguments)-3] {
		name, argument, ok := strings.Cut(value, "=")
		if !ok || argument == "" || !slices.Contains([]string{"--scip-java", "--producer-version", "--metadata-version", "--build-tool"}, name) {
			return options{}, "", fmt.Errorf("unsupported adapter argument %q", value)
		}
		if seen[name] {
			return options{}, "", fmt.Errorf("duplicate adapter argument %s", name)
		}
		seen[name] = true
		switch name {
		case "--scip-java":
			configuration.indexer = argument
		case "--producer-version":
			configuration.version = argument
		case "--metadata-version":
			configuration.metadataVersion = argument
		case "--build-tool":
			configuration.buildTool = strings.ToLower(argument)
		}
	}
	if !versionPattern.MatchString(configuration.version) {
		return options{}, "", errors.New("producer version must be a short release identifier without whitespace")
	}
	if !versionPattern.MatchString(configuration.metadataVersion) {
		return options{}, "", errors.New("metadata version must be a short release identifier without whitespace")
	}
	if configuration.buildTool != "" && !slices.Contains([]string{"auto", "gradle", "maven", "bazel"}, configuration.buildTool) {
		return options{}, "", errors.New("--build-tool must be auto, gradle, maven, or bazel")
	}
	return configuration, operation, nil
}

func describe(output io.Writer, configuration options) error {
	return json.NewEncoder(output).Encode(map[string]any{
		"protocols":          []string{adapter.Protocol},
		"provider":           map[string]string{"name": providerName, "version": configuration.version},
		"languages":          []string{"java", "kotlin"},
		"operations":         []string{"index"},
		"refresh_modes":      []string{"full"},
		"fact_encoding":      adapter.FactEncoding,
		"position_encodings": []string{"utf8-byte"},
		"requires": map[string]any{
			"executables":        []string{"scip-java"},
			"may_run_build_tool": true,
		},
		"claims": map[string]any{
			"inputs": map[string]any{
				"extensions":      []string{".java", ".kt", ".kts"},
				"filenames":       []string{"build.gradle", "build.gradle.kts", "pom.xml", "settings.gradle", "settings.gradle.kts"},
				"project_markers": []string{"build.gradle", "build.gradle.kts", "pom.xml", "settings.gradle", "settings.gradle.kts"},
			},
			"evidence": []string{"exact"}, "invalidation_all_files": true,
		},
	})
}

func requirePermissions(permissions adapter.Permissions) error {
	var missing []string
	if !permissions.BuildTool {
		missing = append(missing, "build_tool")
	}
	if !permissions.Restore {
		missing = append(missing, "restore")
	}
	if !permissions.Network {
		missing = append(missing, "network")
	}
	if !permissions.RunGenerators {
		missing = append(missing, "run_generators")
	}
	if len(missing) != 0 {
		return fmt.Errorf("scip-java requires explicit permissions before it may invoke an untrusted repository build: %s", strings.Join(missing, ", "))
	}
	return nil
}

func resolveProducer(configuration options) (producer, error) {
	path, err := exec.LookPath(configuration.indexer)
	if err != nil {
		return producer{}, fmt.Errorf("find scip-java executable %q (a JDK 17+ runtime is required by the upstream launcher): %w", configuration.indexer, err)
	}
	if absolute, absoluteErr := filepath.Abs(path); absoluteErr == nil {
		path = absolute
	}
	return producer{path: path, version: configuration.version, metadataVersion: configuration.metadataVersion}, nil
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
	temporary, err := os.MkdirTemp("", "weave-jvm-")
	if err != nil {
		return scipimport.Result{}, nil, fmt.Errorf("create private adapter directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	work := filepath.Join(temporary, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return scipimport.Result{}, nil, fmt.Errorf("create private scip-java directory: %w", err)
	}
	indexPath := filepath.Join(temporary, "index.scip")
	arguments := []string{
		"index",
		"--cwd=" + root,
		"--output=" + indexPath,
		"--temporary-directory=" + work,
		// Upstream's Bazel failure helper otherwise extracts a command from
		// tool output and re-runs it through `bash -c`. Weave keeps producer
		// invocation literal and leaves reproduction to the user.
		"--no-bazel-autorun-sandbox-command",
	}
	if configuration.buildTool != "" {
		arguments = append(arguments, "--build-tool="+configuration.buildTool)
	}
	command, err := run(ctx, tool.path, arguments, root)
	diagnostics := toolDiagnostics(command)
	if err != nil {
		return scipimport.Result{}, diagnostics, fmt.Errorf("run scip-java: %w%s", err, stderrSuffix(command.stderr))
	}
	encoded, err := readIndex(indexPath)
	if err != nil {
		return scipimport.Result{}, diagnostics, err
	}
	encoded, err = normalizeProducerMetadata(encoded, tool)
	if err != nil {
		return scipimport.Result{}, diagnostics, fmt.Errorf("validate scip-java index: %w", err)
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
		return scipimport.Result{}, diagnostics, fmt.Errorf("import scip-java index: %w", err)
	}
	if result.Provider != providerName || result.ProviderVersion != tool.version {
		return scipimport.Result{}, diagnostics, fmt.Errorf("SCIP producer is %s %s, want %s %s; configure --producer-version for an intentional alternate release", result.Provider, result.ProviderVersion, providerName, tool.version)
	}
	return result, diagnostics, nil
}

func readIndex(indexPath string) ([]byte, error) {
	info, err := os.Lstat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("inspect scip-java index: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxIndexBytes {
		return nil, fmt.Errorf("scip-java index must be a regular file no larger than %d bytes", maxIndexBytes)
	}
	encoded, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("read scip-java index: %w", err)
	}
	return encoded, nil
}

func normalizeProducerMetadata(encoded []byte, tool producer) ([]byte, error) {
	var index scip.Index
	if err := (proto.UnmarshalOptions{RecursionLimit: 100}).Unmarshal(encoded, &index); err != nil {
		return nil, fmt.Errorf("decode SCIP protobuf: %w", err)
	}
	if index.Metadata == nil || index.Metadata.ToolInfo == nil || index.Metadata.ToolInfo.Name != "scip-java" {
		return nil, errors.New("SCIP metadata must identify scip-java")
	}
	if index.Metadata.ToolInfo.Version != tool.metadataVersion {
		return nil, fmt.Errorf("SCIP metadata version is %q, want %q; configure --metadata-version for an intentional alternate release", index.Metadata.ToolInfo.Version, tool.metadataVersion)
	}
	// The official v0.13.1 launcher embeds 0.0.0-SNAPSHOT. Validate that
	// observed value above, then retain the selected distribution release as
	// Weave's durable provider and stable-unit identity.
	index.Metadata.ToolInfo.Version = tool.version
	result, err := proto.Marshal(&index)
	if err != nil {
		return nil, fmt.Errorf("encode normalized SCIP protobuf: %w", err)
	}
	if len(result) > maxIndexBytes {
		return nil, errors.New("normalized SCIP index exceeds byte limit")
	}
	return result, nil
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
