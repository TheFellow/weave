package scipimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestImportNormalizesUnicodeSymbolsRelationshipsAndLocalScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	index := fixtureIndex(
		fixtureDocument("one.cs", "🚀Name\n", scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart, 2, 6),
		fixtureDocument("two.cs", "🚀Name\n", scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart, 2, 6),
	)
	data, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	importer := Importer{}
	first, err := importer.Import(context.Background(), data, Options{RepositoryRoot: root, RepositoryIdentity: "example/repo"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := importer.Import(context.Background(), data, Options{RepositoryRoot: root, RepositoryIdentity: "example/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Provider != "scip:fixture-indexer" || first.ProviderVersion != "1.2.3" {
		t.Fatalf("provider metadata = %#v", first)
	}
	if len(first.Units) != 2 || first.Units[0].Unit.ID != second.Units[0].Unit.ID || first.Units[0].Unit.InventoryDigest != second.Units[0].Unit.InventoryDigest {
		t.Fatalf("non-deterministic units: %#v / %#v", first, second)
	}
	if first.Units[0].Symbols[0].ID == first.Units[1].Symbols[0].ID {
		t.Fatal("document-local SCIP symbols collided across documents")
	}
	for _, facts := range first.Units {
		if got := facts.Symbols[0].Definition; got.Start.Column != 4 || got.End.Column != 8 || got.Start.Byte != 4 || got.End.Byte != 8 {
			t.Fatalf("UTF-16 range = %#v", got)
		}
		if len(facts.Edges) != 2 || facts.Edges[0].Evidence != graph.EvidenceExact {
			t.Fatalf("relationships = %#v", facts.Edges)
		}
		if facts.Occurrences[0].Role != "definition" || facts.Occurrences[0].SymbolID != facts.Symbols[0].ID {
			t.Fatalf("occurrence = %#v", facts.Occurrences[0])
		}
	}
}

func TestPositionEncodingConversion(t *testing.T) {
	t.Parallel()
	line := []byte("🚀 Woo")
	tests := []struct {
		name     string
		encoding scip.PositionEncoding
		column   int
		want     int
	}{
		{"utf8", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 5, 5},
		{"utf16", scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart, 3, 5},
		{"utf32", scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart, 2, 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := byteColumn(line, test.encoding, test.column)
			if err != nil || got != test.want {
				t.Fatalf("byteColumn() = %d, %v; want %d", got, err, test.want)
			}
		})
	}
	if _, err := byteColumn(line, scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart, 1); err == nil || !strings.Contains(err.Error(), "surrogate") {
		t.Fatalf("split surrogate error = %v", err)
	}
	if _, err := byteColumn(line, scip.PositionEncoding_UnspecifiedPositionEncoding, 0); err == nil || !strings.Contains(err.Error(), "unspecified") {
		t.Fatalf("unspecified encoding error = %v", err)
	}
}

func TestLegacyPositionEncodingRequiresExplicitProducerOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	document := fixtureDocument("legacy.cpp", "🚀Name\n", scip.PositionEncoding_UnspecifiedPositionEncoding, 4, 8)
	index := fixtureIndex(document)
	// Source encoding metadata cannot determine whether the producer counted
	// UTF-8 bytes, UTF-16 units, or UTF-32 units in occurrence ranges.
	index.Metadata.TextDocumentEncoding = scip.TextEncoding_UTF8
	data, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Importer{}).Import(context.Background(), data, Options{RepositoryRoot: root}); err == nil || !strings.Contains(err.Error(), "unspecified") {
		t.Fatalf("legacy index without override error = %v", err)
	}
	result, err := (Importer{}).Import(context.Background(), data, Options{
		RepositoryRoot: root, LegacyPositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Units[0].Occurrences[0].Range
	if got.Start.Column != 4 || got.End.Column != 8 {
		t.Fatalf("legacy UTF-8 range = %#v", got)
	}
}

func TestImportReadsRepositorySourceAndRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	content, err := os.ReadFile("testdata/source.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "source.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	document := fixtureDocument("nested/source.go", "", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 0)
	document.Occurrences = nil
	data, _ := proto.Marshal(fixtureIndex(document))
	result, err := (Importer{}).Import(context.Background(), data, Options{RepositoryRoot: root})
	if err != nil || len(result.Units) != 1 || result.Units[0].Documents[0].ContentHash == "" {
		t.Fatalf("source fallback = %#v, %v", result, err)
	}

	for _, path := range []string{"../secret", "/absolute", "a//b", "a\\b", "."} {
		document := fixtureDocument(path, "x", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 0)
		document.Occurrences = nil
		data, _ := proto.Marshal(fixtureIndex(document))
		if _, err := (Importer{}).Import(context.Background(), data, Options{RepositoryRoot: root}); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}

	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	link := fixtureDocument("link.go", "", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 0)
	link.Occurrences = nil
	data, _ = proto.Marshal(fixtureIndex(link))
	if _, err := (Importer{}).Import(context.Background(), data, Options{RepositoryRoot: root}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestImportCanonicalizesRepeatedGlobalSymbolInformation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	global := "cxx . todo-pkg todo-version gfx/Rect#x(455f465bc33b4cdf)."
	documents := []*scip.Document{
		fixtureDocument("z.cpp", "Name\n", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 4),
		fixtureDocument("a.h", "Name\n", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 4),
	}
	for _, document := range documents {
		document.Occurrences[0].Symbol = global
		document.Symbols[0].Symbol = global
		document.Symbols[0].Relationships = nil
	}
	data, err := proto.Marshal(fixtureIndex(documents...))
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Importer{}).Import(context.Background(), data, Options{RepositoryRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	var symbols int
	var definitionPath string
	for _, facts := range result.Units {
		symbols += len(facts.Symbols)
		if len(facts.Symbols) != 0 {
			definitionPath = facts.Documents[0].Path
		}
	}
	if symbols != 1 || definitionPath != "a.h" {
		t.Fatalf("canonical global symbol count/path = %d/%q, want 1/a.h", symbols, definitionPath)
	}
}

func TestImportRejectsTruncatedOversizedAndDuplicateInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	document := fixtureDocument("fixture.cs", "Name\n", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 4)
	data, _ := proto.Marshal(fixtureIndex(document))
	tests := []struct {
		name     string
		importer Importer
		data     []byte
		index    *scip.Index
		contains string
	}{
		{"truncated", Importer{}, data[:len(data)-1], nil, "decode SCIP"},
		{"oversized", Importer{Limits: Limits{MaxIndexBytes: 8}}, data, nil, "exceeds"},
		{"duplicate document", Importer{}, nil, fixtureIndex(document, document), "duplicate SCIP document"},
		{"duplicate symbol", Importer{}, nil, fixtureIndex(duplicateSymbolDocument()), "duplicate id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.data
			if test.index != nil {
				input, _ = proto.Marshal(test.index)
			}
			result, err := test.importer.Import(context.Background(), input, Options{RepositoryRoot: root})
			if err == nil || len(result.Units) != 0 || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.contains)) {
				t.Fatalf("Import() = %#v, %v; want %q", result, err, test.contains)
			}
		})
	}
}

func fixtureIndex(documents ...*scip.Document) *scip.Index {
	return &scip.Index{
		Metadata:  &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "fixture-indexer", Version: "1.2.3"}},
		Documents: documents,
	}
}

func fixtureDocument(path, text string, encoding scip.PositionEncoding, start, end int32) *scip.Document {
	symbol := "local 0"
	return &scip.Document{
		RelativePath: path, Language: "csharp", Text: text, PositionEncoding: encoding,
		Occurrences: []*scip.Occurrence{{
			TypedRange: &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{Line: 0, StartCharacter: start, EndCharacter: end}},
			Symbol:     symbol, SymbolRoles: int32(scip.SymbolRole_Definition),
		}},
		Symbols: []*scip.SymbolInformation{{
			Symbol: symbol, DisplayName: "Name", Kind: scip.SymbolInformation_Method,
			Relationships: []*scip.Relationship{{Symbol: "local 1", IsImplementation: true, IsReference: true}},
		}},
	}
}

func duplicateSymbolDocument() *scip.Document {
	document := fixtureDocument("fixture.cs", "Name\n", scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, 0, 4)
	document.Symbols = append(document.Symbols, proto.Clone(document.Symbols[0]).(*scip.SymbolInformation))
	return document
}
