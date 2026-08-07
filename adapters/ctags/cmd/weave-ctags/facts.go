package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
)

const maxJSONLineBytes = 1 << 20

type ctagsEntry struct {
	Name      string
	Path      string
	Language  string
	Kind      string
	Scope     string
	ScopeKind string
	Signature string
	TypeRef   string
	Roles     string
	Extras    string
	Line      int
	End       int
}

type ctagsJSONEntry struct {
	Type      string `json:"_type"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Language  string `json:"language"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
	Signature string `json:"signature"`
	TypeRef   string `json:"typeref"`
	Roles     string `json:"roles"`
	Extras    string `json:"extras"`
	Line      int    `json:"line"`
	End       int    `json:"end"`
}

func parseCtagsJSON(output []byte, known map[string]sourceFile) ([]ctagsEntry, error) {
	reader := bufio.NewReaderSize(bytes.NewReader(output), min(maxJSONLineBytes, 64<<10))
	entries := make([]ctagsEntry, 0)
	lineNumber := 0
	for {
		line, err := readBoundedLine(reader, maxJSONLineBytes)
		if len(line) != 0 {
			lineNumber++
			line = bytes.TrimSpace(line)
			if len(line) != 0 {
				var raw ctagsJSONEntry
				if decodeErr := json.Unmarshal(line, &raw); decodeErr != nil {
					return nil, fmt.Errorf("decode Universal Ctags JSON line %d: %w", lineNumber, decodeErr)
				}
				if raw.Type != "tag" {
					return nil, fmt.Errorf("Universal Ctags JSON line %d has unexpected type %q", lineNumber, raw.Type)
				}
				path, normalizeErr := normalizeRepositoryPath(raw.Path)
				if normalizeErr != nil {
					return nil, fmt.Errorf("Universal Ctags path %q: %w", raw.Path, normalizeErr)
				}
				if _, ok := known[path]; !ok {
					return nil, fmt.Errorf("Universal Ctags returned unrequested path %q", path)
				}
				if raw.Name == "" || raw.Language == "" || raw.Kind == "" || raw.Line < 1 {
					return nil, fmt.Errorf("Universal Ctags returned incomplete tag for %q", path)
				}
				if !isDefinition(raw.Roles, raw.Extras) {
					continue
				}
				entries = append(entries, ctagsEntry{
					Name: raw.Name, Path: path, Language: raw.Language, Kind: raw.Kind,
					Scope: raw.Scope, ScopeKind: raw.ScopeKind, Signature: raw.Signature,
					TypeRef: raw.TypeRef, Roles: raw.Roles, Extras: raw.Extras,
					Line: raw.Line, End: raw.End,
				})
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read Universal Ctags JSON: %w", err)
		}
	}
	return entries, nil
}

func readBoundedLine(reader *bufio.Reader, maximum int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maximum {
			return nil, fmt.Errorf("JSON line exceeds %d bytes", maximum)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func isDefinition(roles, extras string) bool {
	if listContains(extras, "reference") {
		return false
	}
	if strings.TrimSpace(roles) == "" {
		return true
	}
	return listContains(roles, "def")
}

func listContains(value, expected string) bool {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if strings.EqualFold(field, expected) {
			return true
		}
	}
	return false
}

func compareEntries(a, b ctagsEntry) int {
	valuesA := []string{a.Path, strings.ToLower(a.Language), a.ScopeKind, a.Scope, a.Kind, a.Name, a.Signature, a.TypeRef, strconv.Itoa(a.Line), strconv.Itoa(a.End)}
	valuesB := []string{b.Path, strings.ToLower(b.Language), b.ScopeKind, b.Scope, b.Kind, b.Name, b.Signature, b.TypeRef, strconv.Itoa(b.Line), strconv.Itoa(b.End)}
	for i := range valuesA {
		if comparison := strings.Compare(valuesA[i], valuesB[i]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func buildFacts(repository string, tool producer, files []sourceFile, entries []ctagsEntry) ([]graph.UnitFacts, error) {
	if repository == "" {
		repository = "local"
	}
	byPath := make(map[string]sourceFile, len(files))
	for _, file := range files {
		byPath[file.path] = file
	}
	type groupKey struct {
		path     string
		language string
	}
	groups := map[groupKey][]ctagsEntry{}
	for _, file := range files {
		groups[groupKey{path: file.path, language: "unknown"}] = nil
	}
	knownLanguage := map[string]bool{}
	for _, entry := range entries {
		if !knownLanguage[entry.Path] {
			delete(groups, groupKey{path: entry.Path, language: "unknown"})
			knownLanguage[entry.Path] = true
		}
		key := groupKey{path: entry.Path, language: strings.ToLower(entry.Language)}
		groups[key] = append(groups[key], entry)
	}
	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b groupKey) int {
		if comparison := strings.Compare(a.path, b.path); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.language, b.language)
	})
	units := make([]graph.UnitFacts, 0, len(keys))
	for _, key := range keys {
		file, ok := byPath[key.path]
		if !ok {
			return nil, fmt.Errorf("missing source snapshot for %q", key.path)
		}
		content, err := os.ReadFile(file.snapshot)
		if err != nil {
			return nil, fmt.Errorf("read private source snapshot %q: %w", key.path, err)
		}
		facts, err := documentFacts(repository, tool, file, key.language, content, groups[key])
		if err != nil {
			return nil, err
		}
		units = append(units, facts)
	}
	return units, nil
}

func documentFacts(repository string, tool producer, file sourceFile, language string, content []byte, entries []ctagsEntry) (graph.UnitFacts, error) {
	unitID := stableID("unit", repository, file.path, language)
	documentID := stableID("document", repository, file.path, language)
	facts := graph.UnitFacts{
		Unit: graph.Unit{
			ID: unitID, Provider: providerName, ProviderVersion: tool.version,
			Language: language, Variant: "definitions",
			InputFingerprint: digest("input", mappingVersion, repository, file.path, language, file.contentHash, tool.version, tool.capabilityDigest),
		},
		Documents: []graph.Document{{
			ID: documentID, UnitID: unitID, Path: file.path, Language: language,
			ContentHash: file.contentHash, Provider: providerName, ProviderVersion: tool.version,
		}},
	}

	slices.SortFunc(entries, compareEntries)
	collisions := map[string]int{}
	for _, entry := range entries {
		sourceRange, err := lineRange(content, entry.Line)
		if err != nil {
			return graph.UnitFacts{}, fmt.Errorf("Universal Ctags tag %q in %s: %w", entry.Name, file.path, err)
		}
		kind := normalizeKind(entry.Kind)
		base := strings.Join([]string{language, entry.ScopeKind, entry.Scope, kind, entry.Name, entry.Signature, entry.TypeRef}, "\x00")
		collisions[base]++
		ordinal := collisions[base]
		stableName := ctagsStableName(repository, file.path, language, entry, ordinal)
		symbolID := stableID("symbol", repository, file.path, language, entry.ScopeKind, entry.Scope, kind, entry.Name, entry.Signature, entry.TypeRef, strconv.Itoa(ordinal))
		facts.Symbols = append(facts.Symbols, graph.Symbol{
			ID: symbolID, UnitID: unitID, StableName: stableName, DisplayName: entry.Name,
			NormalizedName: graph.NormalizeName(entry.Name), Kind: kind, DocumentID: documentID,
			Definition: sourceRange, Provider: providerName, Evidence: graph.EvidenceSyntactic,
		})
		facts.Occurrences = append(facts.Occurrences, graph.Occurrence{
			ID: stableID("occurrence", symbolID, "definition"), UnitID: unitID, SymbolID: symbolID,
			DocumentID: documentID, Role: "definition", Range: sourceRange,
			Provider: providerName, Evidence: graph.EvidenceSyntactic,
		})
	}
	slices.SortFunc(facts.Symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(facts.Occurrences, func(a, b graph.Occurrence) int { return strings.Compare(a.ID, b.ID) })
	facts.Unit.SurfaceFingerprint = symbolSurface(facts.Symbols)
	facts.Unit.InventoryDigest = inventoryDigest(facts)
	return facts, nil
}

func lineRange(content []byte, oneBasedLine int) (graph.Range, error) {
	if oneBasedLine < 1 {
		return graph.Range{}, errors.New("line must be positive")
	}
	line := 1
	start := 0
	for line < oneBasedLine {
		index := bytes.IndexByte(content[start:], '\n')
		if index < 0 {
			return graph.Range{}, fmt.Errorf("line %d is beyond end of file", oneBasedLine)
		}
		start += index + 1
		line++
	}
	end := len(content)
	if index := bytes.IndexByte(content[start:], '\n'); index >= 0 {
		end = start + index
	}
	if end > start && content[end-1] == '\r' {
		end--
	}
	return graph.Range{
		Start: graph.Position{Line: int32(oneBasedLine - 1), Column: 0, Byte: int64(start)},
		End:   graph.Position{Line: int32(oneBasedLine - 1), Column: int32(end - start), Byte: int64(end)},
	}, nil
}

func ctagsStableName(repository, path, language string, entry ctagsEntry, ordinal int) string {
	parts := []string{"ctags", language, repository, path}
	if entry.Scope != "" {
		parts = append(parts, entry.Scope)
	}
	parts = append(parts, normalizeKind(entry.Kind), entry.Name)
	if entry.Signature != "" {
		parts = append(parts, entry.Signature)
	}
	if ordinal > 1 {
		parts = append(parts, "#"+strconv.Itoa(ordinal))
	}
	return strings.Join(parts, " ")
}

func normalizeKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "symbol"
	}
	var result strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			result.WriteRune(r)
			lastDash = false
		default:
			if result.Len() != 0 && !lastDash {
				result.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(result.String(), "-")
}

func stableID(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(mappingVersion))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(kind))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return kind + ":" + hex.EncodeToString(hash.Sum(nil))
}

func digest(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(kind))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func symbolSurface(symbols []graph.Symbol) string {
	hash := sha256.New()
	for _, symbol := range symbols {
		_, _ = hash.Write([]byte(symbol.StableName))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(symbol.Kind))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func inventoryDigest(facts graph.UnitFacts) string {
	hash := sha256.New()
	for _, document := range facts.Documents {
		_, _ = hash.Write([]byte("document\x00" + document.ID + "\x00"))
	}
	for _, symbol := range facts.Symbols {
		_, _ = hash.Write([]byte("symbol\x00" + symbol.ID + "\x00"))
	}
	for _, occurrence := range facts.Occurrences {
		_, _ = hash.Write([]byte("occurrence\x00" + occurrence.ID + "\x00"))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
