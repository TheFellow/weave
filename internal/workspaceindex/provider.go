// Package workspaceindex indexes the Git-visible workspace and structured
// content without executing repository code, builders, templates, or hooks.
package workspaceindex

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
)

const (
	providerName         = "weave-workspace"
	providerVersion      = "1"
	maxInventoryBytes    = 16 << 20
	maxDocumentBytes     = 16 << 20
	maxAllDocumentsBytes = 512 << 20
	maxInventoryPaths    = 250_000
	maxInventoryFacts    = 1_000_000
	maxDocumentFacts     = 250_000
)

var errDocumentTooLarge = errors.New("structured document exceeds size limit")

// Provider is the built-in, source-only workspace provider.
type Provider struct{}

func (Provider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: providerName, Version: providerVersion}
}

type entry struct {
	path   string
	kind   string
	target string
	info   os.FileInfo
}

// Refresh inventories Git-visible paths, parses structured documents, and
// replaces only units whose normalized inputs changed.
func (provider Provider) Refresh(ctx context.Context, request freshness.Request) (freshness.Result, error) {
	entries, err := inventory(ctx, request.Repository.Root)
	if err != nil {
		return freshness.Result{}, err
	}
	models := make(map[string]*documentModel)
	var diagnostics []string
	var total int64
	budgetExhausted := false
	for _, item := range entries {
		if item.kind != "file" || !isMarkdown(item.path) {
			continue
		}
		if budgetExhausted {
			continue
		}
		content, err := readBounded(filepath.Join(request.Repository.Root, filepath.FromSlash(item.path)), item.info, maxDocumentBytes)
		if err != nil {
			if errors.Is(err, errDocumentTooLarge) {
				diagnostics = appendDegradation(diagnostics, item.path, err)
				continue
			}
			return freshness.Result{}, fmt.Errorf("read structured document %q: %w", item.path, err)
		}
		total += int64(len(content))
		if total > maxAllDocumentsBytes {
			budgetExhausted = true
			diagnostics = appendDegradation(diagnostics, item.path, fmt.Errorf("structured corpus exceeds %d bytes; this and subsequent documents use topology only", maxAllDocumentsBytes))
			continue
		}
		model, err := parseDocument(request.Repository.Identity, item.path, content)
		if err != nil {
			diagnostics = appendDegradation(diagnostics, item.path, err)
			continue
		}
		if model.factUpperBound() > maxDocumentFacts {
			diagnostics = appendDegradation(diagnostics, item.path, fmt.Errorf("document exceeds %d facts", maxDocumentFacts))
			continue
		}
		models[item.path] = model
	}

	resolver := newResolver(request.Repository.Identity, entries, models)
	previous := previousUnits(request.Previous)
	result := freshness.Result{Diagnostics: diagnostics}

	inventoryFacts, err := buildInventory(request.Repository.Identity, entries, models, resolver)
	if err != nil {
		return freshness.Result{}, err
	}
	if err := inventoryFacts.Validate(); err != nil {
		return freshness.Result{}, fmt.Errorf("validate workspace inventory: %w", err)
	}
	appendUnit(&result, previous, inventoryFacts, request.Force)
	for _, item := range entries {
		var facts graph.UnitFacts
		if model, ok := models[item.path]; ok {
			facts = model.facts(resolver)
		} else {
			facts = fileFacts(request.Repository.Identity, item)
		}
		if count := len(facts.Documents) + len(facts.Symbols) + len(facts.Occurrences) + len(facts.Edges); count > maxDocumentFacts {
			return freshness.Result{}, fmt.Errorf("unit %q unexpectedly exceeds %d facts", item.path, maxDocumentFacts)
		}
		if err := facts.Validate(); err != nil {
			return freshness.Result{}, fmt.Errorf("validate workspace unit %q: %w", item.path, err)
		}
		appendUnit(&result, previous, facts, request.Force)
	}
	for id := range previous {
		result.Removed = append(result.Removed, id)
	}
	slices.SortFunc(result.Units, func(a, b freshness.Unit) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(result.Removed)
	slices.SortFunc(result.Batches, func(a, b graph.UnitFacts) int { return strings.Compare(a.Unit.ID, b.Unit.ID) })
	return result, nil
}

func appendDegradation(values []string, name string, err error) []string {
	if len(values) >= 256 {
		values[255] = "workspace content degradation diagnostics truncated"
		return values
	}
	message := fmt.Sprintf("workspace content degraded to file topology for %s: %v", name, err)
	if len(message) > 4096 {
		end := 4093
		for end > 0 && !utf8.ValidString(message[:end]) {
			end--
		}
		message = message[:end] + "..."
	}
	return append(values, message)
}

func appendUnit(result *freshness.Result, previous map[string]freshness.Unit, facts graph.UnitFacts, force bool) {
	unit := freshness.Unit{
		ID: facts.Unit.ID, InputFingerprint: facts.Unit.InputFingerprint,
		SurfaceFingerprint: facts.Unit.SurfaceFingerprint, InventoryDigest: facts.Unit.InventoryDigest,
	}
	result.Units = append(result.Units, unit)
	if old, ok := previous[unit.ID]; force || !ok || old != unit {
		result.Batches = append(result.Batches, facts)
	}
	delete(previous, unit.ID)
}

func previousUnits(manifest *freshness.Manifest) map[string]freshness.Unit {
	result := map[string]freshness.Unit{}
	if manifest == nil {
		return result
	}
	for _, unit := range manifest.Units {
		result[unit.ID] = unit
	}
	return result
}

func inventory(ctx context.Context, root string) ([]entry, error) {
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--")
	command.Dir = root
	var stdout limitedBuffer
	stdout.limit = maxInventoryBytes
	var stderr limitedBuffer
	stderr.limit = 64 << 10
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("inventory Git-visible paths: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("Git path inventory exceeds %d bytes", maxInventoryBytes)
	}
	var result []entry
	for _, raw := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		name := filepath.ToSlash(string(raw))
		if !validRepositoryPath(name) {
			return nil, fmt.Errorf("Git returned invalid repository path %q", name)
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect repository path %q: %w", name, err)
		}
		item := entry{path: name, kind: "file", info: info}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.kind = "symlink"
			item.target, err = os.Readlink(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil {
				return nil, fmt.Errorf("read symlink %q: %w", name, err)
			}
		case info.Mode().IsRegular():
		default:
			item.kind = "resource"
		}
		result = append(result, item)
		if len(result) > maxInventoryPaths {
			return nil, fmt.Errorf("Git path inventory exceeds %d entries", maxInventoryPaths)
		}
	}
	slices.SortFunc(result, func(a, b entry) int { return strings.Compare(a.path, b.path) })
	return result, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit + 1 - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.Buffer.Write(value)
	return original, nil
}

func readBounded(name string, expected os.FileInfo, limit int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if expected == nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, fmt.Errorf("path changed identity before read")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%w: %d bytes", errDocumentTooLarge, limit)
	}
	return content, nil
}

func validRepositoryPath(value string) bool {
	return value != "" && value != "." && !strings.HasPrefix(value, "/") && !strings.ContainsRune(value, 0) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func isMarkdown(name string) bool {
	extension := strings.ToLower(path.Ext(name))
	if extension == ".md" || extension == ".markdown" {
		return true
	}
	base := strings.ToLower(path.Base(name))
	return base == "llms.txt" || base == "llms-full.txt"
}

func fileFacts(identity string, item entry) graph.UnitFacts {
	unitID := stableID("unit", identity, item.path)
	kind := item.kind
	if kind == "file" && isAsset(item.path) {
		kind = "asset"
	}
	fingerprint := digest("file", providerVersion, item.path, kind, item.target)
	return graph.UnitFacts{
		Unit: graph.Unit{ID: unitID, Provider: providerName, ProviderVersion: providerVersion, Variant: kind, InputFingerprint: fingerprint, SurfaceFingerprint: fingerprint},
		Symbols: []graph.Symbol{{
			ID: fileSymbolID(identity, item.path), UnitID: unitID, StableName: item.path, DisplayName: path.Base(item.path),
			Kind: kind, Provider: providerName, Evidence: graph.EvidenceExact,
		}},
	}
}

func isAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".pdf", ".woff", ".woff2", ".ttf", ".otf":
		return true
	default:
		return false
	}
}

func buildInventory(identity string, entries []entry, models map[string]*documentModel, resolver *resolver) (graph.UnitFacts, error) {
	return buildInventoryBounded(identity, entries, models, resolver, maxInventoryFacts)
}

func buildInventoryBounded(identity string, entries []entry, models map[string]*documentModel, resolver *resolver, limit int) (graph.UnitFacts, error) {
	unitID := stableID("inventory-unit", identity)
	facts := graph.UnitFacts{Unit: graph.Unit{ID: unitID, Provider: providerName, ProviderVersion: providerVersion, Variant: "inventory"}}
	rootID := workspaceSymbolID(identity)
	facts.Symbols = append(facts.Symbols, graph.Symbol{
		ID: rootID, UnitID: unitID, StableName: ".", DisplayName: identity, Kind: "workspace",
		Provider: providerName, Evidence: graph.EvidenceExact,
	})
	directories := map[string]bool{".": true}
	for _, item := range entries {
		for directory := path.Dir(item.path); directory != "." && !directories[directory]; directory = path.Dir(directory) {
			directories[directory] = true
		}
	}
	var names []string
	for directory := range directories {
		if directory != "." {
			names = append(names, directory)
		}
	}
	slices.Sort(names)
	registries := resolver.registries()
	type alias struct{ from, to string }
	aliases := make([]alias, 0, len(resolver.aliases))
	for from, to := range resolver.aliases {
		aliases = append(aliases, alias{from, to})
	}
	slices.SortFunc(aliases, func(a, b alias) int {
		return strings.Compare(a.from+"\x00"+a.to, b.from+"\x00"+b.to)
	})
	projected := int64(1) + int64(len(names))*2 + int64(len(entries)) + int64(len(registries)) + int64(len(aliases))
	if projected > int64(limit) {
		return graph.UnitFacts{}, fmt.Errorf("workspace inventory requires %d facts, limit is %d", projected, limit)
	}
	for _, directory := range names {
		facts.Symbols = append(facts.Symbols, graph.Symbol{
			ID: directorySymbolID(identity, directory), UnitID: unitID, StableName: directory + "/", DisplayName: path.Base(directory),
			Kind: "directory", Provider: providerName, Evidence: graph.EvidenceExact,
		})
	}
	for _, directory := range names {
		parent := path.Dir(directory)
		facts.Edges = append(facts.Edges, plainEdge(unitID, directoryNodeID(identity, parent), directorySymbolID(identity, directory), graph.EdgeContains, graph.EvidenceExact))
	}
	for _, item := range entries {
		facts.Edges = append(facts.Edges, plainEdge(unitID, directoryNodeID(identity, path.Dir(item.path)), fileSymbolID(identity, item.path), graph.EdgeContains, graph.EvidenceExact))
	}
	for _, value := range registries {
		facts.Symbols = append(facts.Symbols, graph.Symbol{
			ID: value.id, UnitID: unitID, StableName: value.stable, DisplayName: value.display,
			Kind: value.kind, Provider: providerName, Evidence: graph.EvidenceDeclared,
		})
	}
	for _, value := range aliases {
		facts.Edges = append(facts.Edges, plainEdge(unitID, value.from, value.to, graph.EdgeResolvesTo, graph.EvidenceInferred))
	}

	var inventory []string
	for _, item := range entries {
		inventory = append(inventory, item.path, item.kind, item.target)
	}
	for _, model := range models {
		inventory = append(inventory,
			model.path, model.metadata.Permalink, strings.Join(model.metadata.RedirectFrom, "\x00"),
			strings.Join(model.metadata.Topics, "\x00"), strings.Join(model.metadata.Tags, "\x00"),
			strings.Join(model.metadata.Categories, "\x00"), model.metadata.Series,
		)
	}
	for _, value := range resolver.registries() {
		inventory = append(inventory, value.id, value.kind, value.stable, value.display)
	}
	for from, to := range resolver.aliases {
		inventory = append(inventory, from, to)
	}
	slices.Sort(inventory)
	facts.Unit.InputFingerprint = digest(append([]string{"inventory", providerVersion}, inventory...)...)
	facts.Unit.SurfaceFingerprint = resolver.surface
	facts.Unit.InventoryDigest = facts.Unit.InputFingerprint
	slices.SortFunc(facts.Symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(facts.Edges, graph.CompareEdges)
	return facts, nil
}

func factCount(facts graph.UnitFacts) int {
	return len(facts.Documents) + len(facts.Symbols) + len(facts.Occurrences) + len(facts.Edges)
}

func directoryNodeID(identity, directory string) string {
	if directory == "." {
		return workspaceSymbolID(identity)
	}
	return directorySymbolID(identity, directory)
}

func plainEdge(unitID, from, to string, kind graph.EdgeKind, evidence graph.Evidence) graph.Edge {
	return graph.Edge{
		ID: stableID("edge", unitID, from, string(kind), to), UnitID: unitID, From: from, To: to,
		Kind: kind, Evidence: evidence, Provider: providerName,
	}
}

func workspaceSymbolID(identity string) string { return stableID("workspace", identity) }
func directorySymbolID(identity, name string) string {
	return stableID("directory", identity, name)
}
func fileSymbolID(identity, name string) string { return graph.WorkspacePathID(identity, name) }
func sectionSymbolID(identity, name, anchor string) string {
	return stableID("section", identity, name, anchor)
}
func stableID(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "weave-workspace/v1\x00"+kind)
	for _, value := range values {
		_, _ = io.WriteString(hash, "\x00"+value)
	}
	return "workspace-" + kind + ":" + hex.EncodeToString(hash.Sum(nil))
}

func digest(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(hash, value)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
