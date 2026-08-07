// Package schemabuild indexes source-only schemas, infrastructure, migrations,
// and declarative build manifests without executing repository code or tools.
package schemabuild

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
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/relationship"
)

const (
	providerName      = "weave-schema-build"
	providerVersion   = "1"
	maxInventoryBytes = 16 << 20
	maxSourceBytes    = 8 << 20
	maxCorpusBytes    = 256 << 20
	maxFiles          = 50_000
	maxFacts          = 1_000_000
)

// Provider is the built-in, local-only schema and build graph provider.
type Provider struct{}

func (Provider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: providerName, Version: providerVersion}
}

type sourceFile struct {
	path string
	data []byte
	hash string
}

type category struct {
	name  string
	files []sourceFile
}

// Refresh uses category-atomic units. Cross-file schema categories never
// publish partially linked graphs; independently parseable build manifests may
// degrade per file. No failure can affect another category's last good unit.
func (Provider) Refresh(ctx context.Context, request freshness.Request) (freshness.Result, error) {
	files, err := discover(ctx, request.Repository.Root)
	if err != nil {
		return freshness.Result{}, err
	}
	groups := group(files)
	previous := previousUnits(request.Previous)
	result := freshness.Result{}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return freshness.Result{}, err
		}
		unitID := stableID("unit", request.Repository.Identity, group.name)
		fingerprint := categoryFingerprint(group)
		if old, ok := previous[unitID]; ok && !request.Force && old.InputFingerprint == fingerprint {
			result.Units = append(result.Units, old)
			result.Diagnostics = append(result.Diagnostics, previousCategoryDiagnostics(request.Previous, group.name)...)
			delete(previous, unitID)
			continue
		}
		facts, warnings, parseErr := parseCategory(ctx, request.Repository.Identity, request.Repository.Root, group, fingerprint)
		if err := ctx.Err(); err != nil {
			return freshness.Result{}, err
		}
		for _, warning := range warnings {
			result.Diagnostics = appendDiagnostic(result.Diagnostics, group.name+": "+warning)
		}
		if parseErr != nil {
			result.Diagnostics = appendDiagnostic(result.Diagnostics, group.name+" category omitted atomically: "+parseErr.Error())
			continue
		}
		if err := facts.Validate(); err != nil {
			return freshness.Result{}, fmt.Errorf("validate %s category: %w", group.name, err)
		}
		if factCount(facts) > maxFacts {
			result.Diagnostics = appendDiagnostic(result.Diagnostics, fmt.Sprintf("%s category omitted atomically: exceeds %d facts", group.name, maxFacts))
			continue
		}
		unit := freshness.Unit{ID: facts.Unit.ID, InputFingerprint: facts.Unit.InputFingerprint, SurfaceFingerprint: facts.Unit.SurfaceFingerprint, InventoryDigest: facts.Unit.InventoryDigest}
		result.Units = append(result.Units, unit)
		result.Batches = append(result.Batches, facts)
		delete(previous, unitID)
	}
	for id := range previous {
		result.Removed = append(result.Removed, id)
	}
	slices.SortFunc(result.Units, func(a, b freshness.Unit) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Batches, func(a, b graph.UnitFacts) int { return strings.Compare(a.Unit.ID, b.Unit.ID) })
	slices.Sort(result.Removed)
	slices.Sort(result.Diagnostics)
	result.Diagnostics = slices.Compact(result.Diagnostics)
	return result, nil
}

func previousCategoryDiagnostics(manifest *freshness.Manifest, category string) []string {
	if manifest == nil {
		return nil
	}
	var result []string
	prefix := category + ": "
	for _, diagnostic := range manifest.Diagnostics {
		diagnostic = strings.TrimPrefix(diagnostic, providerName+": ")
		if strings.HasPrefix(diagnostic, prefix) {
			result = append(result, diagnostic)
		}
	}
	return result
}

func previousUnits(manifest *freshness.Manifest) map[string]freshness.Unit {
	result := map[string]freshness.Unit{}
	if manifest != nil {
		for _, unit := range manifest.Units {
			result[unit.ID] = unit
		}
	}
	return result
}

func appendDiagnostic(values []string, value string) []string {
	if len(value) > 4096 {
		end := 4093
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		value = value[:end] + "..."
	}
	if len(values) < 256 {
		return append(values, value)
	}
	values[255] = "schema/build diagnostics truncated"
	return values
}

func discover(ctx context.Context, root string) ([]sourceFile, error) {
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--")
	command.Dir = root
	var stdout boundedBuffer
	stdout.limit = maxInventoryBytes
	var stderr boundedBuffer
	stderr.limit = 64 << 10
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("inventory schema/build paths: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("Git path inventory exceeds %d bytes", maxInventoryBytes)
	}
	var result []sourceFile
	var total int64
	for _, raw := range bytes.Split(stdout.Bytes(), []byte{0}) {
		name := filepath.ToSlash(string(raw))
		if name == "" || !supported(name) {
			continue
		}
		if !validPath(name) {
			return nil, fmt.Errorf("Git returned invalid repository path %q", name)
		}
		full := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := readBounded(full, info)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", name, err)
		}
		total += int64(len(data))
		if total > maxCorpusBytes {
			return nil, fmt.Errorf("schema/build source corpus exceeds %d bytes", maxCorpusBytes)
		}
		digest := sha256.Sum256(data)
		result = append(result, sourceFile{path: name, data: data, hash: "sha256:" + hex.EncodeToString(digest[:])})
		if len(result) > maxFiles {
			return nil, fmt.Errorf("schema/build source inventory exceeds %d files", maxFiles)
		}
	}
	slices.SortFunc(result, func(a, b sourceFile) int { return strings.Compare(a.path, b.path) })
	return result, nil
}

func readBounded(name string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, errors.New("path changed identity before read")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds %d bytes", maxSourceBytes)
	}
	return data, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := buffer.limit + 1 - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.Buffer.Write(data)
	if buffer.Len() > buffer.limit {
		buffer.exceeded = true
	}
	return original, nil
}

func validPath(name string) bool {
	return name != "." && name != ".." && name != "" && !strings.HasPrefix(name, "/") && !strings.ContainsRune(name, 0) && path.Clean(name) == name && !strings.HasPrefix(name, "../")
}

func supported(name string) bool { return classify(name) != "" }

func classify(name string) string {
	lower := strings.ToLower(name)
	base := path.Base(lower)
	switch {
	case path.Ext(lower) == ".proto":
		return "protobuf"
	case isOpenAPIPath(base):
		return "openapi"
	case path.Ext(lower) == ".graphql" || path.Ext(lower) == ".gql":
		return "graphql"
	case isMigrationPath(lower):
		return "sql-migrations"
	case path.Ext(lower) == ".tf":
		return "terraform"
	case isBuildManifest(lower):
		return "build"
	default:
		return ""
	}
}

func isOpenAPIPath(base string) bool {
	extension := path.Ext(base)
	if extension != ".yaml" && extension != ".yml" && extension != ".json" {
		return false
	}
	return strings.HasPrefix(base, "openapi") || strings.HasPrefix(base, "swagger") || strings.Contains(base, ".openapi.")
}

func isMigrationPath(name string) bool {
	if path.Ext(name) != ".sql" {
		return false
	}
	for _, segment := range strings.Split(path.Dir(name), "/") {
		if segment == "migration" || segment == "migrations" || segment == "migrate" {
			return true
		}
	}
	base := path.Base(name)
	return (strings.HasPrefix(base, "v") || (base[0] >= '0' && base[0] <= '9')) && strings.Contains(base, "__")
}

func isBuildManifest(name string) bool {
	base := path.Base(name)
	if base == "go.mod" || base == "cargo.toml" || base == "package.json" || base == "pom.xml" {
		return true
	}
	ext := path.Ext(base)
	return ext == ".csproj" || ext == ".fsproj" || ext == ".vbproj"
}

func group(files []sourceFile) []category {
	byName := map[string][]sourceFile{}
	for _, file := range files {
		byName[classify(file.path)] = append(byName[classify(file.path)], file)
	}
	var result []category
	for _, name := range []string{"protobuf", "openapi", "graphql", "sql-migrations", "terraform", "build"} {
		if len(byName[name]) != 0 {
			result = append(result, category{name: name, files: byName[name]})
		}
	}
	return result
}

func categoryFingerprint(group category) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "weave-schema-build/category/v1\x00"+providerVersion+"\x00"+group.name)
	for _, file := range group.files {
		_, _ = io.WriteString(hash, "\x00"+file.path+"\x00"+file.hash)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func factCount(facts graph.UnitFacts) int {
	return len(facts.Documents) + len(facts.Symbols) + len(facts.Occurrences) + len(facts.Edges)
}

func stableID(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "weave-schema-build/v1\x00"+kind)
	for _, value := range values {
		_, _ = io.WriteString(hash, "\x00"+value)
	}
	return "schema-" + kind + ":" + hex.EncodeToString(hash.Sum(nil))
}

type factBuilder struct {
	repository  string
	facts       graph.UnitFacts
	documents   map[string]graph.Document
	symbols     map[string]bool
	occurrences map[string]bool
	edges       map[string]bool
}

func newFactBuilder(repository string, group category, fingerprint string) *factBuilder {
	unitID := stableID("unit", repository, group.name)
	builder := &factBuilder{
		repository: repository, documents: map[string]graph.Document{}, symbols: map[string]bool{}, occurrences: map[string]bool{}, edges: map[string]bool{},
		facts: graph.UnitFacts{Unit: graph.Unit{ID: unitID, Provider: providerName, ProviderVersion: providerVersion, Language: group.name, Variant: "source-only", InputFingerprint: fingerprint, InventoryDigest: fingerprint}},
	}
	for _, file := range group.files {
		documentID := stableID("document", repository, group.name, file.path)
		document := graph.Document{ID: documentID, UnitID: unitID, Path: file.path, Language: group.name, ContentHash: file.hash, Provider: providerName, ProviderVersion: providerVersion}
		builder.documents[file.path] = document
		builder.facts.Documents = append(builder.facts.Documents, document)
	}
	return builder
}

func (builder *factBuilder) localID(domain, stableName string) string {
	return stableID("symbol", builder.repository, domain, stableName)
}

func openID(domain, stableName string) string { return stableID("open", domain, stableName) }

func (builder *factBuilder) addSymbol(domain, stableName, display, kind, file string, sourceRange graph.Range, evidence graph.Evidence) string {
	id := builder.localID(domain, stableName)
	if builder.symbols[id] {
		builder.addDefinitionOccurrence(id, file, sourceRange, evidence)
		return id
	}
	builder.symbols[id] = true
	symbol := graph.Symbol{ID: id, UnitID: builder.facts.Unit.ID, StableName: stableName, DisplayName: display, NormalizedName: graph.NormalizeName(display), Kind: kind, Provider: providerName, Evidence: evidence}
	if document, ok := builder.documents[file]; ok {
		symbol.DocumentID = document.ID
		symbol.Definition = sourceRange
	}
	builder.facts.Symbols = append(builder.facts.Symbols, symbol)
	builder.addDefinitionOccurrence(id, file, sourceRange, evidence)
	return id
}

func (builder *factBuilder) addDefinitionOccurrence(id, file string, sourceRange graph.Range, evidence graph.Evidence) {
	document, ok := builder.documents[file]
	if !ok {
		return
	}
	occurrenceID := stableID("occurrence", builder.facts.Unit.ID, id, document.ID, "definition", rangeKey(sourceRange))
	if !builder.occurrences[occurrenceID] {
		builder.occurrences[occurrenceID] = true
		builder.facts.Occurrences = append(builder.facts.Occurrences, graph.Occurrence{ID: occurrenceID, UnitID: builder.facts.Unit.ID, SymbolID: id, DocumentID: document.ID, Role: "definition", Range: sourceRange, Provider: providerName, Evidence: evidence})
	}
	builder.addEdge(graph.WorkspacePathID(builder.repository, file), id, graph.EdgeDefines, file, sourceRange, evidence)
}

func (builder *factBuilder) addReference(symbolID, file string, sourceRange graph.Range, evidence graph.Evidence) {
	document, ok := builder.documents[file]
	if !ok {
		return
	}
	occurrenceID := stableID("occurrence", builder.facts.Unit.ID, symbolID, document.ID, "reference", rangeKey(sourceRange))
	if builder.occurrences[occurrenceID] {
		return
	}
	builder.occurrences[occurrenceID] = true
	builder.facts.Occurrences = append(builder.facts.Occurrences, graph.Occurrence{ID: occurrenceID, UnitID: builder.facts.Unit.ID, SymbolID: symbolID, DocumentID: document.ID, Role: "reference", Range: sourceRange, Provider: providerName, Evidence: evidence})
}

func (builder *factBuilder) addEdge(from, to string, kind graph.EdgeKind, file string, sourceRange graph.Range, evidence graph.Evidence) {
	edgeID := stableID("edge", builder.facts.Unit.ID, from, string(kind), to, file, rangeKey(sourceRange))
	if builder.edges[edgeID] {
		return
	}
	builder.edges[edgeID] = true
	spec := relationship.Spec{ID: edgeID, From: from, To: to, Kind: kind, Evidence: evidence}
	if document, ok := builder.documents[file]; ok {
		spec.DocumentID, spec.Range = document.ID, sourceRange
	}
	builder.facts.Edges = append(builder.facts.Edges, (relationship.Builder{UnitID: builder.facts.Unit.ID, Provider: providerName, Evidence: evidence}).MustBuild(spec))
}

func (builder *factBuilder) finish() graph.UnitFacts {
	slices.SortFunc(builder.facts.Documents, func(a, b graph.Document) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(builder.facts.Symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(builder.facts.Occurrences, func(a, b graph.Occurrence) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(builder.facts.Edges, graph.CompareEdges)
	hash := sha256.New()
	for _, symbol := range builder.facts.Symbols {
		_, _ = io.WriteString(hash, symbol.ID+"\x00"+symbol.StableName+"\x00"+symbol.Kind+"\x00")
	}
	for _, edge := range builder.facts.Edges {
		_, _ = io.WriteString(hash, edge.From+"\x00"+string(edge.Kind)+"\x00"+edge.To+"\x00"+string(edge.Evidence)+"\x00")
	}
	builder.facts.Unit.SurfaceFingerprint = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return builder.facts
}

func rangeKey(value graph.Range) string {
	return fmt.Sprintf("%d:%d:%d-%d:%d:%d", value.Start.Line, value.Start.Column, value.Start.Byte, value.End.Line, value.End.Column, value.End.Byte)
}

func byteRange(data []byte, start, end int) graph.Range {
	if start < 0 || end < start || end > len(data) {
		return unknownRange()
	}
	line, column := offsetPosition(data, start)
	endLine, endColumn := offsetPosition(data, end)
	return graph.Range{Start: graph.Position{Line: int32(line), Column: int32(column), Byte: int64(start)}, End: graph.Position{Line: int32(endLine), Column: int32(endColumn), Byte: int64(end)}}
}

func offsetPosition(data []byte, offset int) (int, int) {
	lineStart, line := 0, 0
	for index, value := range data[:offset] {
		if value == '\n' {
			line, lineStart = line+1, index+1
		}
	}
	return line, offset - lineStart
}

func lineColumnRange(data []byte, line, column, length int) graph.Range {
	if line < 1 || column < 1 {
		return unknownRange()
	}
	offset, currentLine := 0, 1
	for currentLine < line && offset < len(data) {
		if data[offset] == '\n' {
			currentLine++
		}
		offset++
	}
	if currentLine != line {
		return unknownRange()
	}
	for count := 1; count < column; count++ {
		if offset >= len(data) || data[offset] == '\n' {
			return unknownRange()
		}
		_, size := utf8.DecodeRune(data[offset:])
		offset += size
	}
	return byteRange(data, offset, min(offset+max(length, 0), len(data)))
}

func unknownRange() graph.Range {
	return graph.Range{Start: graph.Position{Byte: -1}, End: graph.Position{Byte: -1}}
}

func parseCategory(ctx context.Context, repository, root string, group category, fingerprint string) (graph.UnitFacts, []string, error) {
	builder := newFactBuilder(repository, group, fingerprint)
	var warnings []string
	var err error
	switch group.name {
	case "protobuf":
		warnings, err = parseProtobuf(ctx, builder, group.files)
	case "openapi":
		warnings, err = parseOpenAPI(builder, group.files)
	case "graphql":
		warnings, err = parseGraphQL(builder, group.files)
	case "sql-migrations":
		warnings, err = parseSQL(builder, group.files)
	case "terraform":
		warnings, err = parseTerraform(builder, group.files)
	case "build":
		warnings, err = parseBuild(builder, root, group.files)
	default:
		err = fmt.Errorf("unknown category %q", group.name)
	}
	if err != nil {
		return graph.UnitFacts{}, warnings, err
	}
	return builder.finish(), warnings, nil
}
