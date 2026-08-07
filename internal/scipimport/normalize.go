package scipimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/relationship"
	"github.com/scip-code/scip/bindings/go/scip"
)

type normalizedOccurrence struct {
	symbol     string
	symbolID   string
	role       string
	source     graph.Range
	definition bool
}

func normalizeDocument(document *scip.Document, source []byte, identity, path, provider, providerVersion string, positionEncoding scip.PositionEncoding, limits Limits) (graph.UnitFacts, error) {
	if len(document.Language) > limits.MaxStringBytes || !utf8.ValidString(document.Language) {
		return graph.UnitFacts{}, errors.New("invalid or oversized language")
	}
	unitID := stableID("scip-unit:", identity, path, provider, providerVersion)
	documentID := stableID("scip-document:", identity, provider, path)
	contentHash := sha256.Sum256(source)
	facts := graph.UnitFacts{
		Unit: graph.Unit{ID: unitID, Provider: provider, ProviderVersion: providerVersion, Language: document.Language},
		Documents: []graph.Document{{
			ID: documentID, UnitID: unitID, Path: path, Language: document.Language,
			ContentHash: "sha256:" + hex.EncodeToString(contentHash[:]), Provider: provider, ProviderVersion: providerVersion,
		}},
	}

	occurrences := make([]normalizedOccurrence, 0, len(document.Occurrences))
	definitions := map[string]graph.Range{}
	for number, occurrence := range document.Occurrences {
		if occurrence == nil || occurrence.Symbol == "" {
			continue
		}
		if len(occurrence.Symbol) > limits.MaxStringBytes {
			return graph.UnitFacts{}, fmt.Errorf("occurrence %d symbol exceeds string limit", number)
		}
		if _, err := scip.ParseSymbol(occurrence.Symbol); err != nil {
			return graph.UnitFacts{}, fmt.Errorf("occurrence %d invalid symbol: %w", number, err)
		}
		scipRange, ok := occurrence.SourceRange()
		if !ok {
			return graph.UnitFacts{}, fmt.Errorf("occurrence %d has no valid source range", number)
		}
		sourceRange, err := convertRange(source, positionEncoding, scipRange)
		if err != nil {
			return graph.UnitFacts{}, fmt.Errorf("occurrence %d range: %w", number, err)
		}
		definition := hasRole(occurrence, scip.SymbolRole_Definition) || hasRole(occurrence, scip.SymbolRole_ForwardDefinition)
		role := "reference"
		if definition {
			role = "definition"
			if _, exists := definitions[occurrence.Symbol]; !exists {
				definitions[occurrence.Symbol] = sourceRange
			}
		}
		occurrences = append(occurrences, normalizedOccurrence{
			symbol: occurrence.Symbol, symbolID: symbolID(identity, provider, path, occurrence.Symbol), role: role,
			source: sourceRange, definition: definition,
		})
	}

	for number, information := range document.Symbols {
		if information == nil || information.Symbol == "" {
			return graph.UnitFacts{}, fmt.Errorf("symbol information %d has no symbol", number)
		}
		if len(information.Symbol) > limits.MaxStringBytes || len(information.DisplayName) > limits.MaxStringBytes {
			return graph.UnitFacts{}, fmt.Errorf("symbol information %d exceeds string limit", number)
		}
		parsed, err := scip.ParseSymbol(information.Symbol)
		if err != nil {
			return graph.UnitFacts{}, fmt.Errorf("symbol information %d invalid symbol: %w", number, err)
		}
		display, kind := symbolPresentation(information, parsed)
		symbol := graph.Symbol{
			ID: symbolID(identity, provider, path, information.Symbol), UnitID: unitID, StableName: information.Symbol,
			DisplayName: display, Kind: kind, Provider: provider, Evidence: graph.EvidenceExact,
		}
		if definition, ok := definitions[information.Symbol]; ok {
			symbol.DocumentID, symbol.Definition = documentID, definition
		}
		facts.Symbols = append(facts.Symbols, symbol)
		for relationNumber, relationship := range information.Relationships {
			if relationship == nil || relationship.Symbol == "" {
				return graph.UnitFacts{}, fmt.Errorf("symbol information %d relationship %d has no symbol", number, relationNumber)
			}
			if _, err := scip.ParseSymbol(relationship.Symbol); err != nil {
				return graph.UnitFacts{}, fmt.Errorf("relationship target invalid symbol: %w", err)
			}
			target := symbolID(identity, provider, path, relationship.Symbol)
			if relationship.IsImplementation {
				facts.Edges = append(facts.Edges, semanticEdge(unitID, symbol.ID, target, graph.EdgeImplements, provider))
			}
			if relationship.IsReference || relationship.IsDefinition || relationship.IsTypeDefinition {
				facts.Edges = append(facts.Edges, semanticEdge(unitID, symbol.ID, target, graph.EdgeReferences, provider))
			}
		}
	}
	for _, occurrence := range occurrences {
		id := stableID("scip-occurrence:", unitID, occurrence.symbol, occurrence.role, rangeKey(occurrence.source))
		facts.Occurrences = append(facts.Occurrences, graph.Occurrence{
			ID: id, UnitID: unitID, SymbolID: occurrence.symbolID, DocumentID: documentID,
			Role: occurrence.role, Range: occurrence.source, Provider: provider, Evidence: graph.EvidenceExact,
		})
	}

	sortFacts(&facts)
	facts.Unit.InputFingerprint = "sha256:" + hex.EncodeToString(contentHash[:])
	facts.Unit.SurfaceFingerprint = digestSurface(facts.Symbols, facts.Edges)
	facts.Unit.InventoryDigest = digestInventory(facts)
	if err := facts.Validate(); err != nil {
		return graph.UnitFacts{}, err
	}
	return facts, nil
}

func symbolPresentation(information *scip.SymbolInformation, parsed *scip.Symbol) (string, string) {
	display := information.DisplayName
	kind := ""
	if information.Kind != scip.SymbolInformation_UnspecifiedKind {
		kind = strings.ToLower(information.Kind.String())
	}
	if len(parsed.Descriptors) != 0 {
		descriptor := parsed.Descriptors[len(parsed.Descriptors)-1]
		if display == "" {
			display = descriptor.Name
		}
		if kind == "" {
			kind = descriptorKind(descriptor.Suffix)
		}
	}
	if display == "" {
		display = information.Symbol
	}
	if kind == "" {
		kind = "symbol"
	}
	return display, kind
}

func descriptorKind(suffix scip.Descriptor_Suffix) string {
	switch suffix {
	case scip.Descriptor_Namespace:
		return "namespace"
	case scip.Descriptor_Type:
		return "type"
	case scip.Descriptor_Term:
		return "term"
	case scip.Descriptor_Method:
		return "method"
	case scip.Descriptor_TypeParameter:
		return "typeparameter"
	case scip.Descriptor_Parameter:
		return "parameter"
	case scip.Descriptor_Meta:
		return "meta"
	case scip.Descriptor_Local:
		return "local"
	case scip.Descriptor_Macro:
		return "macro"
	default:
		return "symbol"
	}
}

func hasRole(occurrence *scip.Occurrence, role scip.SymbolRole) bool {
	return occurrence.SymbolRoles&int32(role) != 0
}

func symbolID(identity, provider, path, symbol string) string {
	if scip.IsLocalSymbol(symbol) {
		return stableID("scip-symbol:", identity, provider, path, symbol)
	}
	return stableID("scip-symbol:", identity, provider, symbol)
}

func semanticEdge(unitID, from, to string, kind graph.EdgeKind, provider string) graph.Edge {
	return (relationship.Builder{UnitID: unitID, Provider: provider, Evidence: graph.EvidenceExact}).MustBuild(relationship.Spec{
		ID: stableID("scip-edge:", unitID, from, string(kind), to), From: from, To: to, Kind: kind,
	})
}

func convertRange(source []byte, encoding scip.PositionEncoding, value scip.Range) (graph.Range, error) {
	if err := value.Validate(); err != nil {
		return graph.Range{}, err
	}
	start, err := convertPosition(source, encoding, value.Start)
	if err != nil {
		return graph.Range{}, err
	}
	end, err := convertPosition(source, encoding, value.End)
	if err != nil {
		return graph.Range{}, err
	}
	result := graph.Range{Start: start, End: end}
	return result, result.Validate()
}

func convertPosition(source []byte, encoding scip.PositionEncoding, position scip.Position) (graph.Position, error) {
	if position.Line < 0 || position.Character < 0 {
		return graph.Position{}, errors.New("negative source coordinate")
	}
	line, offset, ok := sourceLine(source, int(position.Line))
	if !ok {
		return graph.Position{}, fmt.Errorf("line %d exceeds source", position.Line)
	}
	column, err := byteColumn(line, encoding, int(position.Character))
	if err != nil {
		return graph.Position{}, err
	}
	return graph.Position{Line: position.Line, Column: int32(column), Byte: int64(offset + column)}, nil
}

func sourceLine(source []byte, target int) ([]byte, int, bool) {
	start, line := 0, 0
	for index, value := range source {
		if value != '\n' {
			continue
		}
		if line == target {
			end := index
			if end > start && source[end-1] == '\r' {
				end--
			}
			return source[start:end], start, true
		}
		line++
		start = index + 1
	}
	if line == target {
		end := len(source)
		if end > start && source[end-1] == '\r' {
			end--
		}
		return source[start:end], start, true
	}
	return nil, 0, false
}

func byteColumn(line []byte, encoding scip.PositionEncoding, target int) (int, error) {
	if !utf8.Valid(line) {
		return 0, errors.New("source line is not valid UTF-8")
	}
	switch encoding {
	case scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart:
		if target > len(line) || (target < len(line) && !utf8.RuneStart(line[target])) {
			return 0, errors.New("UTF-8 column is outside source or splits a code point")
		}
		return target, nil
	case scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart:
		units, bytes := 0, 0
		for bytes < len(line) {
			if units == target {
				return bytes, nil
			}
			r, size := utf8.DecodeRune(line[bytes:])
			units += utf16.RuneLen(r)
			bytes += size
			if units > target {
				return 0, errors.New("UTF-16 column splits a surrogate pair")
			}
		}
		if units == target {
			return bytes, nil
		}
	case scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart:
		units, bytes := 0, 0
		for bytes < len(line) {
			if units == target {
				return bytes, nil
			}
			_, size := utf8.DecodeRune(line[bytes:])
			units++
			bytes += size
		}
		if units == target {
			return bytes, nil
		}
	case scip.PositionEncoding_UnspecifiedPositionEncoding:
		return 0, errors.New("SCIP document has ambiguous unspecified position encoding")
	default:
		return 0, fmt.Errorf("unsupported SCIP position encoding %d", encoding)
	}
	return 0, errors.New("column exceeds source line")
}

func sortFacts(facts *graph.UnitFacts) {
	slices.SortFunc(facts.Symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(facts.Occurrences, func(a, b graph.Occurrence) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(facts.Edges, graph.CompareEdges)
}

func digestSurface(symbols []graph.Symbol, edges []graph.Edge) string {
	value, _ := json.Marshal(struct {
		Symbols []graph.Symbol `json:"symbols"`
		Edges   []graph.Edge   `json:"edges"`
	}{symbols, edges})
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestInventory(facts graph.UnitFacts) string {
	facts.Unit.InventoryDigest = ""
	value, _ := json.Marshal(facts)
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func rangeKey(value graph.Range) string {
	return fmt.Sprintf("%d:%d:%d:%d", value.Start.Line, value.Start.Column, value.End.Line, value.End.Column)
}

type globalSymbolCandidate struct {
	symbol graph.Symbol
	unit   int
	path   string
}

// SCIP permits a producer to repeat global SymbolInformation in documents
// that use the symbol. Weave stores one canonical symbol fact and retains every
// occurrence. Conflicting semantic descriptions still reject the whole index.
func deduplicateGlobalSymbols(units []graph.UnitFacts) error {
	selected := map[string]globalSymbolCandidate{}
	for unitNumber := range units {
		documentPaths := make(map[string]string, len(units[unitNumber].Documents))
		for _, document := range units[unitNumber].Documents {
			documentPaths[document.ID] = document.Path
		}
		for _, symbol := range units[unitNumber].Symbols {
			candidate := globalSymbolCandidate{symbol: symbol, unit: unitNumber, path: documentPaths[symbol.DocumentID]}
			current, exists := selected[symbol.ID]
			if !exists {
				selected[symbol.ID] = candidate
				continue
			}
			if !equivalentGlobalSymbol(current.symbol, symbol) {
				return fmt.Errorf("conflicting duplicate SCIP symbol %q", symbol.StableName)
			}
			if preferGlobalSymbol(candidate, current, units) {
				selected[symbol.ID] = candidate
			}
		}
	}

	for unitNumber := range units {
		facts := &units[unitNumber]
		symbols := facts.Symbols[:0]
		for _, symbol := range facts.Symbols {
			if selected[symbol.ID].unit == unitNumber {
				symbols = append(symbols, symbol)
			}
		}
		if len(symbols) == len(facts.Symbols) {
			continue
		}
		facts.Symbols = symbols
		sortFacts(facts)
		facts.Unit.SurfaceFingerprint = digestSurface(facts.Symbols, facts.Edges)
		facts.Unit.InventoryDigest = digestInventory(*facts)
		if err := facts.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func equivalentGlobalSymbol(left, right graph.Symbol) bool {
	return left.StableName == right.StableName && left.DisplayName == right.DisplayName &&
		left.NormalizedName == right.NormalizedName && left.Kind == right.Kind &&
		left.Provider == right.Provider && left.Evidence == right.Evidence
}

func preferGlobalSymbol(candidate, current globalSymbolCandidate, units []graph.UnitFacts) bool {
	candidateDefined, currentDefined := candidate.symbol.DocumentID != "", current.symbol.DocumentID != ""
	if candidateDefined != currentDefined {
		return candidateDefined
	}
	if candidate.path != current.path {
		return candidate.path < current.path
	}
	return units[candidate.unit].Unit.ID < units[current.unit].Unit.ID
}

func validateGlobalIDs(units []graph.UnitFacts) error {
	seen := map[string]string{}
	check := func(id, kind string) error {
		if previous, ok := seen[id]; ok {
			return fmt.Errorf("duplicate SCIP fact ID %q for %s and %s", id, previous, kind)
		}
		seen[id] = kind
		return nil
	}
	for _, unit := range units {
		if err := check(unit.Unit.ID, "unit"); err != nil {
			return err
		}
		for _, document := range unit.Documents {
			if err := check(document.ID, "document"); err != nil {
				return err
			}
		}
		for _, symbol := range unit.Symbols {
			if err := check(symbol.ID, "symbol"); err != nil {
				return err
			}
		}
		for _, occurrence := range unit.Occurrences {
			if err := check(occurrence.ID, "occurrence"); err != nil {
				return err
			}
		}
		for _, edge := range unit.Edges {
			if err := check(edge.ID, "edge"); err != nil {
				return err
			}
		}
	}
	return nil
}
