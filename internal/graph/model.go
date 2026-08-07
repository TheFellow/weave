// Package graph defines Weave's language-neutral semantic fact model.
package graph

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SchemaVersion is the current normalized fact schema stored by Weave.
const SchemaVersion uint32 = 1

// Evidence describes how a fact was established.
type Evidence string

const (
	EvidenceExact     Evidence = "exact"
	EvidenceDeclared  Evidence = "declared"
	EvidenceGenerated Evidence = "generated"
	EvidenceInferred  Evidence = "inferred"
	EvidenceSyntactic Evidence = "syntactic"
	EvidenceAmbiguous Evidence = "ambiguous"
)

// EdgeKind is a directed semantic relationship from one stable symbol to another.
type EdgeKind string

const (
	EdgeDefines      EdgeKind = "defines"
	EdgeReferences   EdgeKind = "references"
	EdgeCalls        EdgeKind = "calls"
	EdgeImports      EdgeKind = "imports"
	EdgeContains     EdgeKind = "contains"
	EdgeExtends      EdgeKind = "extends"
	EdgeImplements   EdgeKind = "implements"
	EdgeInstantiates EdgeKind = "instantiates"
	EdgeDependsOn    EdgeKind = "depends-on"
	EdgeTests        EdgeKind = "tests"
	EdgeGenerates    EdgeKind = "generates"
	EdgeDocuments    EdgeKind = "documents"
	EdgeExposes      EdgeKind = "exposes"
	EdgeHandles      EdgeKind = "handles"
	EdgeReads        EdgeKind = "reads"
	EdgeWrites       EdgeKind = "writes"
	EdgeLinksTo      EdgeKind = "links-to"
	EdgeEmbeds       EdgeKind = "embeds"
	EdgeMemberOf     EdgeKind = "member-of"
	EdgeResolvesTo   EdgeKind = "resolves-to"
)

// Position is a zero-based UTF-8 byte line/column position. Byte is the
// optional zero-based document byte offset; -1 means unavailable.
type Position struct {
	Line   int32 `json:"line"`
	Column int32 `json:"column"`
	Byte   int64 `json:"byte"`
}

// Range is half-open: Start is included and End is excluded.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Unit is the atomic semantic replacement and invalidation boundary.
type Unit struct {
	ID                 string `json:"id"`
	Provider           string `json:"provider"`
	ProviderVersion    string `json:"provider_version"`
	Language           string `json:"language,omitempty"`
	Variant            string `json:"variant,omitempty"`
	InputFingerprint   string `json:"input_fingerprint,omitempty"`
	SurfaceFingerprint string `json:"surface_fingerprint,omitempty"`
	InventoryDigest    string `json:"inventory_digest,omitempty"`
}

// Document is source owned by one compilation unit.
type Document struct {
	ID              string `json:"id"`
	UnitID          string `json:"unit_id"`
	Path            string `json:"path"`
	Language        string `json:"language"`
	ContentHash     string `json:"content_hash,omitempty"`
	Provider        string `json:"provider"`
	ProviderVersion string `json:"provider_version"`
}

// Symbol is a stable semantic entity, optionally with a definition location.
type Symbol struct {
	ID             string `json:"id"`
	UnitID         string `json:"unit_id"`
	StableName     string `json:"stable_name"`
	DisplayName    string `json:"display_name"`
	NormalizedName string `json:"normalized_name"`
	Kind           string `json:"kind"`
	DocumentID     string `json:"document_id,omitempty"`
	// Definition is the canonical display anchor. All binding sites are retained
	// as definition occurrences because some languages permit repeated bindings.
	Definition Range    `json:"definition"`
	Provider   string   `json:"provider"`
	Evidence   Evidence `json:"evidence"`
}

// Occurrence is a definition or reference to a symbol in source.
type Occurrence struct {
	ID         string   `json:"id"`
	UnitID     string   `json:"unit_id"`
	SymbolID   string   `json:"symbol_id"`
	DocumentID string   `json:"document_id"`
	Role       string   `json:"role"`
	Range      Range    `json:"range"`
	Provider   string   `json:"provider"`
	Evidence   Evidence `json:"evidence"`
}

// Edge is one directed relationship. Endpoints may be external symbols not
// defined in the local database.
type Edge struct {
	ID         string   `json:"id"`
	UnitID     string   `json:"unit_id"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Kind       EdgeKind `json:"kind"`
	Evidence   Evidence `json:"evidence"`
	DocumentID string   `json:"document_id,omitempty"`
	Range      Range    `json:"range"`
	Provider   string   `json:"provider"`
}

// UnitFacts is one complete, atomically replaceable semantic unit.
type UnitFacts struct {
	Unit        Unit         `json:"unit"`
	Documents   []Document   `json:"documents"`
	Symbols     []Symbol     `json:"symbols"`
	Occurrences []Occurrence `json:"occurrences"`
	Edges       []Edge       `json:"edges"`
}

// Snapshot is a deterministic full logical export of a database.
type Snapshot struct {
	Schema      uint32       `json:"schema_version"`
	Units       []Unit       `json:"units"`
	Documents   []Document   `json:"documents"`
	Symbols     []Symbol     `json:"symbols"`
	Occurrences []Occurrence `json:"occurrences"`
	Edges       []Edge       `json:"edges"`
}

// Validate checks a complete unit before any persistent state is changed.
func (f UnitFacts) Validate() error {
	if err := validText("unit id", f.Unit.ID, true); err != nil {
		return err
	}
	if err := validText("unit provider", f.Unit.Provider, true); err != nil {
		return err
	}
	if err := validText("provider version", f.Unit.ProviderVersion, true); err != nil {
		return err
	}

	documents := make(map[string]struct{}, len(f.Documents))
	if err := validateOwned(f.Unit.ID, f.Documents, documents, func(v Document) (string, string) { return v.ID, v.UnitID }); err != nil {
		return fmt.Errorf("documents: %w", err)
	}
	for _, document := range f.Documents {
		if err := validText("document path", document.Path, true); err != nil {
			return fmt.Errorf("document %q: %w", document.ID, err)
		}
		if err := validText("document provider", document.Provider, true); err != nil {
			return fmt.Errorf("document %q: %w", document.ID, err)
		}
	}

	symbols := make(map[string]struct{}, len(f.Symbols))
	if err := validateOwned(f.Unit.ID, f.Symbols, symbols, func(v Symbol) (string, string) { return v.ID, v.UnitID }); err != nil {
		return fmt.Errorf("symbols: %w", err)
	}
	for i := range f.Symbols {
		symbol := &f.Symbols[i]
		if err := validText("stable name", symbol.StableName, true); err != nil {
			return fmt.Errorf("symbol %q: %w", symbol.ID, err)
		}
		if err := validText("display name", symbol.DisplayName, true); err != nil {
			return fmt.Errorf("symbol %q: %w", symbol.ID, err)
		}
		if symbol.NormalizedName == "" {
			symbol.NormalizedName = NormalizeName(symbol.DisplayName)
		}
		if symbol.DocumentID != "" {
			if _, ok := documents[symbol.DocumentID]; !ok {
				return fmt.Errorf("symbol %q: unknown document %q", symbol.ID, symbol.DocumentID)
			}
			if err := symbol.Definition.Validate(); err != nil {
				return fmt.Errorf("symbol %q definition: %w", symbol.ID, err)
			}
		}
		if !validEvidence(symbol.Evidence) {
			return fmt.Errorf("symbol %q: invalid evidence %q", symbol.ID, symbol.Evidence)
		}
	}

	occurrences := map[string]struct{}{}
	if err := validateOwned(f.Unit.ID, f.Occurrences, occurrences, func(v Occurrence) (string, string) { return v.ID, v.UnitID }); err != nil {
		return fmt.Errorf("occurrences: %w", err)
	}
	for _, occurrence := range f.Occurrences {
		if occurrence.SymbolID == "" {
			return fmt.Errorf("occurrence %q: symbol id is empty", occurrence.ID)
		}
		if _, ok := documents[occurrence.DocumentID]; !ok {
			return fmt.Errorf("occurrence %q: unknown document %q", occurrence.ID, occurrence.DocumentID)
		}
		if err := occurrence.Range.Validate(); err != nil {
			return fmt.Errorf("occurrence %q: %w", occurrence.ID, err)
		}
		if !validEvidence(occurrence.Evidence) {
			return fmt.Errorf("occurrence %q: invalid evidence %q", occurrence.ID, occurrence.Evidence)
		}
	}

	edges := map[string]struct{}{}
	if err := validateOwned(f.Unit.ID, f.Edges, edges, func(v Edge) (string, string) { return v.ID, v.UnitID }); err != nil {
		return fmt.Errorf("edges: %w", err)
	}
	for _, edge := range f.Edges {
		if edge.From == "" || edge.To == "" {
			return fmt.Errorf("edge %q: endpoints must be nonempty", edge.ID)
		}
		if !validEdgeKind(edge.Kind) {
			return fmt.Errorf("edge %q: invalid kind %q", edge.ID, edge.Kind)
		}
		if !validEvidence(edge.Evidence) {
			return fmt.Errorf("edge %q: invalid evidence %q", edge.ID, edge.Evidence)
		}
		if edge.DocumentID != "" {
			if _, ok := documents[edge.DocumentID]; !ok {
				return fmt.Errorf("edge %q: unknown document %q", edge.ID, edge.DocumentID)
			}
			if err := edge.Range.Validate(); err != nil {
				return fmt.Errorf("edge %q: %w", edge.ID, err)
			}
		}
	}
	return nil
}

// Validate checks that a range has nonnegative coordinates and ordered ends.
func (r Range) Validate() error {
	if r.Start.Line < 0 || r.Start.Column < 0 || r.End.Line < 0 || r.End.Column < 0 {
		return errors.New("negative source coordinate")
	}
	if r.Start.Byte < -1 || r.End.Byte < -1 {
		return errors.New("invalid byte offset")
	}
	if r.End.Line < r.Start.Line || (r.End.Line == r.Start.Line && r.End.Column < r.Start.Column) {
		return errors.New("end precedes start")
	}
	if r.Start.Byte >= 0 && r.End.Byte >= 0 && r.End.Byte < r.Start.Byte {
		return errors.New("end byte precedes start byte")
	}
	return nil
}

func validateOwned[T any](unit string, values []T, seen map[string]struct{}, identity func(T) (string, string)) error {
	for _, value := range values {
		id, owner := identity(value)
		if err := validText("id", id, true); err != nil {
			return err
		}
		if owner != unit {
			return fmt.Errorf("fact %q belongs to unit %q, want %q", id, owner, unit)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validText(name, value string, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains NUL", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if len(value) > 1<<20 {
		return fmt.Errorf("%s exceeds 1 MiB", name)
	}
	return nil
}

func validEvidence(value Evidence) bool {
	return slices.Contains([]Evidence{EvidenceExact, EvidenceDeclared, EvidenceGenerated, EvidenceInferred, EvidenceSyntactic, EvidenceAmbiguous}, value)
}

func validEdgeKind(value EdgeKind) bool {
	return slices.Contains([]EdgeKind{EdgeDefines, EdgeReferences, EdgeCalls, EdgeImports, EdgeContains, EdgeExtends, EdgeImplements, EdgeInstantiates, EdgeDependsOn, EdgeTests, EdgeGenerates, EdgeDocuments, EdgeExposes, EdgeHandles, EdgeReads, EdgeWrites, EdgeLinksTo, EdgeEmbeds, EdgeMemberOf, EdgeResolvesTo}, value)
}

// IsEdgeKind reports whether value is part of the version-one edge vocabulary.
func IsEdgeKind(value EdgeKind) bool { return validEdgeKind(value) }

// NormalizeName produces the language-neutral searchable spelling of a name.
func NormalizeName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// Tokens extracts stable lowercase identifier tokens.
func Tokens(value string) []string {
	var tokens []string
	var current []rune
	var previous rune
	flush := func() {
		if len(current) == 0 {
			return
		}
		tokens = append(tokens, strings.ToLower(string(current)))
		current = current[:0]
	}
	runes := []rune(value)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			previous = 0
			continue
		}
		nextIsLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		boundary := len(current) > 0 && ((unicode.IsLower(previous) && unicode.IsUpper(r)) ||
			(unicode.IsUpper(previous) && unicode.IsUpper(r) && nextIsLower) ||
			(unicode.IsDigit(previous) != unicode.IsDigit(r)))
		if boundary {
			flush()
		}
		current = append(current, r)
		previous = r
	}
	flush()
	slices.Sort(tokens)
	return slices.Compact(tokens)
}

// SortSnapshot applies the canonical export ordering in place.
func SortSnapshot(snapshot *Snapshot) {
	slices.SortFunc(snapshot.Units, func(a, b Unit) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(snapshot.Documents, func(a, b Document) int { return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Path, b.Path)) })
	slices.SortFunc(snapshot.Symbols, func(a, b Symbol) int { return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.StableName, b.StableName)) })
	slices.SortFunc(snapshot.Occurrences, func(a, b Occurrence) int {
		return cmp.Or(cmp.Compare(a.SymbolID, b.SymbolID), cmp.Compare(a.DocumentID, b.DocumentID), compareRange(a.Range, b.Range), cmp.Compare(a.Role, b.Role), cmp.Compare(a.ID, b.ID))
	})
	slices.SortFunc(snapshot.Edges, CompareEdges)
}

// CompareEdges defines the canonical edge ordering.
func CompareEdges(a, b Edge) int {
	return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.To, b.To), cmp.Compare(a.Evidence, b.Evidence), cmp.Compare(a.ID, b.ID))
}

func compareRange(a, b Range) int {
	return cmp.Or(cmp.Compare(a.Start.Line, b.Start.Line), cmp.Compare(a.Start.Column, b.Start.Column), cmp.Compare(a.End.Line, b.End.Line), cmp.Compare(a.End.Column, b.End.Column))
}
