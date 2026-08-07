// weave-ctags adapts Universal Ctags definitions to weave.adapter/v0.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/graph"
)

const (
	providerName         = "ctags:universal-ctags"
	defaultVersion       = "6.2.1"
	mappingVersion       = "weave-ctags/v1"
	maxGitOutputBytes    = 16 << 20
	maxToolOutputBytes   = 64 << 20
	maxToolStderrBytes   = 1 << 20
	maxProbeOutputBytes  = 8 << 20
	maxFileBytes         = 16 << 20
	maxAggregateBytes    = 512 << 20
	maxVisibleFiles      = 100_000
	maxDiagnostics       = 256
	maxArgumentsPerBatch = 128
	maxArgumentBytes     = 32 << 10
	toolTimeout          = 30 * time.Second
)

var (
	versionPattern   = regexp.MustCompile(`(?m)^Universal Ctags ([^,\s]+)`)
	versionIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_()\-]{0,127}$`)
)

type options struct {
	ctags   string
	version string
}

type producer struct {
	path             string
	version          string
	capabilityDigest string
}

type outputLimits struct {
	stdout int
	stderr int
}

type commandResult struct {
	stdout []byte
	stderr []byte
}

type commandRunner func(context.Context, string, []string, string, []string, outputLimits) (commandResult, error)
type pathResolver func(string) (string, error)

type dependencies struct {
	run      commandRunner
	lookPath pathResolver
	exePath  func() (string, error)
}

type sourceFile struct {
	path        string
	snapshot    string
	contentHash string
}

type indexResult struct {
	units       []graph.UnitFacts
	diagnostics []adapter.Diagnostic
}

func main() {
	deps := dependencies{run: runCommand, lookPath: exec.LookPath, exePath: os.Executable}
	if err := runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, deps); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "weave-ctags: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, arguments []string, input io.Reader, output io.Writer, deps dependencies) error {
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
	tool, err := resolveProducer(ctx, configuration, deps)
	if err != nil {
		return err
	}
	gitPath, err := deps.lookPath("git")
	if err != nil {
		return fmt.Errorf("find git executable: %w", err)
	}
	result, err := index(ctx, request, tool, gitPath, deps.run)
	if err != nil {
		return err
	}
	return writeResult(output, request, tool, result)
}

func parseArguments(arguments []string) (options, string, error) {
	configuration := options{ctags: os.Getenv("WEAVE_CTAGS"), version: os.Getenv("WEAVE_CTAGS_VERSION")}
	if configuration.version == "" {
		configuration.version = defaultVersion
	}
	if len(arguments) < 3 || arguments[len(arguments)-2] != "--protocol" || arguments[len(arguments)-1] != adapter.Protocol {
		return options{}, "", errors.New("usage: weave-ctags [--ctags=PATH] [--producer-version=VERSION] (describe|index) --protocol weave.adapter/v0")
	}
	operation := arguments[len(arguments)-3]
	if operation != "describe" && operation != "index" {
		return options{}, "", errors.New("operation must be describe or index")
	}
	seen := map[string]bool{}
	for _, value := range arguments[:len(arguments)-3] {
		name, argument, ok := strings.Cut(value, "=")
		if !ok || argument == "" || !slices.Contains([]string{"--ctags", "--producer-version"}, name) {
			return options{}, "", fmt.Errorf("unsupported adapter argument %q", value)
		}
		if seen[name] {
			return options{}, "", fmt.Errorf("duplicate adapter argument %s", name)
		}
		seen[name] = true
		switch name {
		case "--ctags":
			configuration.ctags = argument
		case "--producer-version":
			configuration.version = argument
		}
	}
	if !versionIDPattern.MatchString(configuration.version) {
		return options{}, "", errors.New("producer version must be a short Universal Ctags release identifier without whitespace")
	}
	return configuration, operation, nil
}

func describe(output io.Writer, configuration options) error {
	return jsonEncode(output, map[string]any{
		"protocols":          []string{adapter.Protocol},
		"provider":           map[string]string{"name": providerName, "version": configuration.version},
		"languages":          []string{"*"},
		"operations":         []string{"index"},
		"refresh_modes":      []string{"full"},
		"fact_encoding":      adapter.FactEncoding,
		"position_encodings": []string{"utf8-byte"},
		"requires": map[string]any{
			"executables":        []string{"git", "Universal Ctags"},
			"may_run_build_tool": false,
		},
		"claims": map[string]any{
			"inputs":   map[string]any{"extensions": []string{".*"}},
			"evidence": []string{"syntactic"}, "fallback": true,
		},
	})
}

func resolveProducer(ctx context.Context, configuration options, deps dependencies) (producer, error) {
	path, err := resolveCtagsPath(configuration.ctags, deps)
	if err != nil {
		return producer{}, err
	}
	versionResult, err := runProbe(ctx, deps.run, path, []string{"--options=NONE", "--version"})
	if err != nil {
		return producer{}, fmt.Errorf("probe Universal Ctags version: %w%s", err, stderrSuffix(versionResult.stderr))
	}
	match := versionPattern.FindSubmatch(versionResult.stdout)
	if len(match) != 2 {
		return producer{}, fmt.Errorf("helper is not Universal Ctags: %q", strings.TrimSpace(string(versionResult.stdout)))
	}
	actual := string(match[1])
	if actual != configuration.version {
		return producer{}, fmt.Errorf("Universal Ctags version is %q, want %q; configure --producer-version for an intentional alternate release", actual, configuration.version)
	}

	features, err := runProbe(ctx, deps.run, path, []string{"--options=NONE", "--list-features"})
	if err != nil {
		return producer{}, fmt.Errorf("inspect Universal Ctags features: %w%s", err, stderrSuffix(features.stderr))
	}
	if !hasListEntry(features.stdout, "json") {
		return producer{}, errors.New("Universal Ctags helper was built without JSON support")
	}
	formats, err := runProbe(ctx, deps.run, path, []string{"--options=NONE", "--list-output-formats"})
	if err != nil {
		return producer{}, fmt.Errorf("inspect Universal Ctags output formats: %w%s", err, stderrSuffix(formats.stderr))
	}
	if !hasListEntry(formats.stdout, "json") {
		return producer{}, errors.New("Universal Ctags helper does not advertise JSON output")
	}
	languages, err := runProbe(ctx, deps.run, path, []string{"--options=NONE", "--list-languages"})
	if err != nil {
		return producer{}, fmt.Errorf("inspect Universal Ctags languages: %w%s", err, stderrSuffix(languages.stderr))
	}
	if len(nonCommentLines(languages.stdout)) == 0 {
		return producer{}, errors.New("Universal Ctags helper advertises no language parsers")
	}
	inventories := []struct {
		name      string
		arguments []string
		output    []byte
	}{
		{name: "mappings", arguments: []string{"--options=NONE", "--list-maps"}},
		{name: "kinds", arguments: []string{"--options=NONE", "--list-kinds-full=all"}},
		{name: "roles", arguments: []string{"--options=NONE", "--list-roles=all"}},
		{name: "fields", arguments: []string{"--options=NONE", "--list-fields"}},
		{name: "extras", arguments: []string{"--options=NONE", "--list-extras"}},
	}
	for index := range inventories {
		result, probeErr := runProbe(ctx, deps.run, path, inventories[index].arguments)
		if probeErr != nil {
			return producer{}, fmt.Errorf("inspect Universal Ctags %s: %w%s", inventories[index].name, probeErr, stderrSuffix(result.stderr))
		}
		inventories[index].output = result.stdout
	}
	digest := sha256.New()
	for _, value := range [][]byte{versionResult.stdout, features.stdout, formats.stdout, languages.stdout} {
		_, _ = digest.Write(bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n")))
		_, _ = digest.Write([]byte{0})
	}
	for _, inventory := range inventories {
		_, _ = digest.Write([]byte(inventory.name))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(bytes.ReplaceAll(inventory.output, []byte("\r\n"), []byte("\n")))
		_, _ = digest.Write([]byte{0})
	}
	return producer{path: path, version: actual, capabilityDigest: "sha256:" + hex.EncodeToString(digest.Sum(nil))}, nil
}

func resolveCtagsPath(configured string, deps dependencies) (string, error) {
	if configured != "" {
		path, err := deps.lookPath(configured)
		if err != nil {
			return "", fmt.Errorf("find Universal Ctags executable %q: %w", configured, err)
		}
		return absolutePath(path), nil
	}
	if deps.exePath != nil {
		if executable, err := deps.exePath(); err == nil {
			name := "uctags"
			if filepath.Ext(executable) != "" {
				name += filepath.Ext(executable)
			}
			candidate := filepath.Join(filepath.Dir(executable), name)
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
				return absolutePath(candidate), nil
			}
		}
	}
	for _, name := range []string{"uctags", "universal-ctags", "ctags"} {
		if path, err := deps.lookPath(name); err == nil {
			return absolutePath(path), nil
		}
	}
	return "", errors.New("find Universal Ctags executable: set WEAVE_CTAGS or --ctags to a pinned helper")
}

func index(ctx context.Context, request adapter.IndexRequest, tool producer, gitPath string, run commandRunner) (indexResult, error) {
	root, err := canonicalDirectory(request.RepositoryRoot)
	if err != nil {
		return indexResult{}, err
	}
	paths, err := gitVisibleFiles(ctx, root, gitPath, run)
	if err != nil {
		return indexResult{}, err
	}
	paths, err = selectInputPaths(paths, request.InputPaths)
	if err != nil {
		return indexResult{}, err
	}
	temporary, err := os.MkdirTemp("", "weave-ctags-")
	if err != nil {
		return indexResult{}, fmt.Errorf("create private Ctags snapshot: %w", err)
	}
	defer os.RemoveAll(temporary)
	mirror := filepath.Join(temporary, "source")
	if err := os.Mkdir(mirror, 0o700); err != nil {
		return indexResult{}, fmt.Errorf("create private source mirror: %w", err)
	}
	files, diagnostics, err := snapshotFiles(root, mirror, paths)
	if err != nil {
		return indexResult{}, err
	}
	entries, toolDiagnostics, err := invokeCtags(ctx, tool, mirror, files, run)
	for _, diagnostic := range toolDiagnostics {
		appendDiagnostic(&diagnostics, diagnostic)
	}
	if err != nil {
		return indexResult{}, err
	}
	units, err := buildFacts(request.RepositoryIdentity, tool, files, entries)
	if err != nil {
		return indexResult{}, err
	}
	facts := 0
	for _, unit := range units {
		facts += len(unit.Documents) + len(unit.Symbols) + len(unit.Occurrences) + len(unit.Edges)
		if facts > request.Limits.MaxFacts {
			return indexResult{}, errors.New("Ctags facts exceed host max_facts")
		}
		if err := unit.Validate(); err != nil {
			return indexResult{}, fmt.Errorf("validate Ctags unit %q: %w", unit.Unit.ID, err)
		}
	}
	return indexResult{units: units, diagnostics: diagnostics}, nil
}

func selectInputPaths(visible, requested []string) ([]string, error) {
	if requested == nil {
		return visible, nil
	}
	selected := make(map[string]bool, len(requested))
	for _, path := range requested {
		normalized, err := normalizeRepositoryPath(path)
		if err != nil || normalized != path {
			return nil, fmt.Errorf("invalid routed input path %q", path)
		}
		selected[path] = true
	}
	result := make([]string, 0, min(len(visible), len(selected)))
	for _, path := range visible {
		if selected[path] {
			result = append(result, path)
		}
	}
	return result, nil
}

func gitVisibleFiles(ctx context.Context, root, gitPath string, run commandRunner) ([]string, error) {
	arguments := []string{"-c", "core.fsmonitor=false", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}
	result, err := runToolWithLimits(ctx, run, gitPath, arguments, root, nil, outputLimits{stdout: maxGitOutputBytes, stderr: maxToolStderrBytes})
	if err != nil {
		return nil, fmt.Errorf("list Git-visible files: %w%s", err, stderrSuffix(result.stderr))
	}
	fields := bytes.Split(result.stdout, []byte{0})
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) == 0 {
			continue
		}
		if !utf8.Valid(field) {
			return nil, errors.New("Git-visible path is not UTF-8")
		}
		value, err := normalizeRepositoryPath(string(field))
		if err != nil {
			return nil, fmt.Errorf("invalid Git-visible path %q: %w", string(field), err)
		}
		paths = append(paths, value)
		if len(paths) > maxVisibleFiles {
			return nil, fmt.Errorf("Git-visible file count exceeds %d", maxVisibleFiles)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func snapshotFiles(root, mirror string, paths []string) ([]sourceFile, []adapter.Diagnostic, error) {
	files := make([]sourceFile, 0, len(paths))
	diagnostics := make([]adapter.Diagnostic, 0)
	var aggregate int64
	for _, path := range paths {
		local, err := filepath.Localize(path)
		if err != nil {
			return nil, nil, fmt.Errorf("localize Git path %q: %w", path, err)
		}
		source := filepath.Join(root, local)
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("inspect Git-visible path %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		resolvedBefore, err := containedSourcePath(root, source)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve Git-visible path %q: %w", path, err)
		}
		if info.Size() > maxFileBytes {
			appendDiagnostic(&diagnostics, adapter.Diagnostic{Severity: "warning", Message: fmt.Sprintf("skipped %s: file exceeds %d bytes", path, maxFileBytes)})
			continue
		}
		content, err := readStableRegularFile(source, info)
		if err != nil {
			return nil, nil, fmt.Errorf("snapshot Git-visible path %q: %w", path, err)
		}
		if !utf8.Valid(content) {
			appendDiagnostic(&diagnostics, adapter.Diagnostic{Severity: "warning", Message: "skipped " + path + ": source is not UTF-8"})
			continue
		}
		resolvedAfter, err := containedSourcePath(root, source)
		if err != nil || resolvedAfter != resolvedBefore {
			return nil, nil, fmt.Errorf("Git-visible path %q changed containment while being snapshotted", path)
		}
		aggregate += int64(len(content))
		if aggregate > maxAggregateBytes {
			return nil, nil, fmt.Errorf("Git-visible source bytes exceed %d", maxAggregateBytes)
		}
		target := filepath.Join(mirror, local)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, nil, fmt.Errorf("create source mirror directory: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return nil, nil, fmt.Errorf("write private source snapshot: %w", err)
		}
		digest := sha256.Sum256(content)
		files = append(files, sourceFile{path: path, snapshot: target, contentHash: "sha256:" + hex.EncodeToString(digest[:])})
	}
	return files, diagnostics, nil
}

func containedSourcePath(root, source string) (string, error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		return "", errors.New("path resolves outside the repository")
	}
	return filepath.Clean(resolved), nil
}

func readStableRegularFile(path string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, errors.New("path changed identity before snapshot")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxFileBytes {
		return nil, fmt.Errorf("file exceeds %d bytes while reading", maxFileBytes)
	}
	return content, nil
}

func invokeCtags(ctx context.Context, tool producer, mirror string, files []sourceFile, run commandRunner) ([]ctagsEntry, []adapter.Diagnostic, error) {
	known := make(map[string]sourceFile, len(files))
	arguments := make([]string, 0, len(files))
	for _, file := range files {
		known[file.path] = file
		local, err := filepath.Localize(file.path)
		if err != nil {
			return nil, nil, err
		}
		// The explicit dot prefix keeps a repository file beginning with a dash
		// from being interpreted as another Ctags option.
		arguments = append(arguments, "."+string(filepath.Separator)+local)
	}
	batches := argumentBatches(arguments)
	entries := make([]ctagsEntry, 0)
	diagnostics := make([]adapter.Diagnostic, 0)
	totalOutput := 0
	for _, batch := range batches {
		base := []string{
			"--options=NONE",
			"--quiet",
			"--output-format=json",
			"--sort=no",
			"--fields=-P+n+e+K+l+Z+p+r+E+S+t+i",
			"--extras=-q",
			"--extras=-r",
			"-o", "-",
		}
		command := append(base, batch...)
		result, err := runTool(ctx, run, tool.path, command, mirror, ctagsEnvironment())
		if len(bytes.TrimSpace(result.stderr)) != 0 {
			appendDiagnostic(&diagnostics, adapter.Diagnostic{Severity: "warning", Message: boundedDiagnostic(result.stderr)})
		}
		if err != nil {
			return nil, diagnostics, fmt.Errorf("run Universal Ctags: %w%s", err, stderrSuffix(result.stderr))
		}
		totalOutput += len(result.stdout)
		if totalOutput > maxToolOutputBytes {
			return nil, diagnostics, fmt.Errorf("Universal Ctags output exceeds %d bytes", maxToolOutputBytes)
		}
		parsed, err := parseCtagsJSON(result.stdout, known)
		if err != nil {
			return nil, diagnostics, err
		}
		entries = append(entries, parsed...)
	}
	slices.SortFunc(entries, compareEntries)
	entries = slices.CompactFunc(entries, func(a, b ctagsEntry) bool { return compareEntries(a, b) == 0 })
	return entries, diagnostics, nil
}

func argumentBatches(arguments []string) [][]string {
	var batches [][]string
	current := make([]string, 0, min(len(arguments), maxArgumentsPerBatch))
	bytesUsed := 0
	for _, argument := range arguments {
		if len(current) != 0 && (len(current) == maxArgumentsPerBatch || bytesUsed+len(argument)+1 > maxArgumentBytes) {
			batches = append(batches, current)
			current = make([]string, 0, min(len(arguments), maxArgumentsPerBatch))
			bytesUsed = 0
		}
		current = append(current, argument)
		bytesUsed += len(argument) + 1
	}
	if len(current) != 0 {
		batches = append(batches, current)
	}
	return batches
}

func runTool(ctx context.Context, run commandRunner, path string, arguments []string, directory string, environment []string) (commandResult, error) {
	return runToolWithLimits(ctx, run, path, arguments, directory, environment, outputLimits{stdout: maxToolOutputBytes, stderr: maxToolStderrBytes})
}

func runProbe(ctx context.Context, run commandRunner, path string, arguments []string) (commandResult, error) {
	return runToolWithLimits(ctx, run, path, arguments, "", ctagsEnvironment(), outputLimits{stdout: maxProbeOutputBytes, stderr: maxToolStderrBytes})
}

func runToolWithLimits(ctx context.Context, run commandRunner, path string, arguments []string, directory string, environment []string, limits outputLimits) (commandResult, error) {
	toolCtx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	result, err := run(toolCtx, path, arguments, directory, environment, limits)
	if toolCtx.Err() != nil {
		return result, toolCtx.Err()
	}
	return result, err
}

func runCommand(ctx context.Context, path string, arguments []string, directory string, environment []string, limits outputLimits) (commandResult, error) {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Dir = directory
	command.WaitDelay = 2 * time.Second
	if environment != nil {
		command.Env = environment
	}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = limits.stdout, limits.stderr
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

func ctagsEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(name, "CTAGS") || strings.EqualFold(name, "ETAGS") {
			continue
		}
		environment = append(environment, value)
	}
	return environment
}

func hasListEntry(output []byte, expected string) bool {
	for _, line := range nonCommentLines(output) {
		fields := strings.Fields(line)
		if len(fields) != 0 && strings.EqualFold(fields[0], expected) {
			return true
		}
	}
	return false
}

func nonCommentLines(output []byte) []string {
	var result []string
	for _, raw := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line != "" && !strings.HasPrefix(line, "#") {
			result = append(result, line)
		}
	}
	return result
}

func normalizeRepositoryPath(value string) (string, error) {
	value = filepath.ToSlash(value)
	value = strings.TrimPrefix(value, "./")
	if value == "" || value == "." || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("path is empty")
	}
	local, err := filepath.Localize(value)
	if err != nil || !filepath.IsLocal(local) {
		return "", errors.New("path must be repository-relative")
	}
	clean := filepath.ToSlash(filepath.Clean(local))
	if clean != value {
		return "", errors.New("path is not normalized")
	}
	return clean, nil
}

func canonicalDirectory(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", errors.New("repository_root must be absolute")
	}
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

func absolutePath(value string) string {
	if result, err := filepath.Abs(value); err == nil {
		return result
	}
	return value
}

func boundedDiagnostic(value []byte) string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(value), "�"))
	if len(text) > 64<<10 {
		text = text[:64<<10]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

func appendDiagnostic(diagnostics *[]adapter.Diagnostic, diagnostic adapter.Diagnostic) {
	if len(*diagnostics) < maxDiagnostics-1 {
		*diagnostics = append(*diagnostics, diagnostic)
		return
	}
	if len(*diagnostics) == maxDiagnostics-1 {
		*diagnostics = append(*diagnostics, adapter.Diagnostic{
			Severity: "warning",
			Message:  "additional Universal Ctags adapter diagnostics were omitted",
		})
	}
}

func stderrSuffix(value []byte) string {
	text := strings.TrimSpace(string(value))
	if text == "" {
		return ""
	}
	return ": stderr: " + text
}
