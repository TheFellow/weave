package workspaceindex

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
)

func TestProviderIndexesWorkspaceAndStructuredContent(t *testing.T) {
	root := fixtureRepository(t)
	provider := Provider{}
	request := freshness.Request{Repository: repository.Repository{Root: root, Identity: "github.com/TheFellow/example"}}
	result, err := provider.Refresh(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batches) != len(result.Units) || len(result.Batches) < 7 {
		t.Fatalf("first refresh batches=%d units=%d", len(result.Batches), len(result.Units))
	}
	for _, facts := range result.Batches {
		if err := facts.Validate(); err != nil {
			t.Fatalf("unit %s is invalid: %v", facts.Unit.ID, err)
		}
	}

	readme := unitForPath(t, result.Batches, "README.md")
	assertSymbol(t, readme.Symbols, "document", "README.md")
	assertSymbol(t, readme.Symbols, "section", "README.md#overview")
	assertSymbol(t, readme.Symbols, "section", "README.md#api-1")
	assertSymbol(t, readme.Symbols, "section", "README.md#html:html-section")
	assertSymbol(t, readme.Symbols, "code-block", "README.md#code-1")
	assertEdge(t, readme.Edges, graph.EdgeLinksTo, fileSymbolID(request.Repository.Identity, "docs/guide.md"))
	assertEdge(t, readme.Edges, graph.EdgeLinksTo, sectionSymbolID(request.Repository.Identity, "docs/guide.md", "details"))
	githubURL := "https://github.com/TheFellow/other/blob/main/docs/design.md#model"
	assertEdge(t, readme.Edges, graph.EdgeLinksTo, stableID("url", "url:"+githubURL))
	assertEdge(t, readme.Edges, graph.EdgeEmbeds, fileSymbolID(request.Repository.Identity, "assets/picture.png"))
	assertEdge(t, readme.Edges, graph.EdgeEmbeds, fileSymbolID(request.Repository.Identity, "assets/badge.svg"))
	assertEdge(t, readme.Edges, graph.EdgeMemberOf, stableID("series", request.Repository.Identity, "series:design notes"))
	assertEdge(t, readme.Edges, graph.EdgeExposes, stableID("route", request.Repository.Identity, "/readme/"))
	for _, edge := range readme.Edges {
		if edge.DocumentID != "" && (edge.Range.Start.Byte < 0 || edge.Range.End.Byte < edge.Range.Start.Byte) {
			t.Fatalf("invalid source range: %#v", edge)
		}
	}
	code := unitForSymbol(t, result.Batches, fileSymbolID(request.Repository.Identity, "docs/code.go"))
	if len(code.Symbols) != 1 || !slices.Contains(code.Symbols[0].SearchTerms, "package") || !slices.Contains(code.Symbols[0].SearchTerms, "docs") {
		t.Fatalf("generic lexical file = %#v", code.Symbols)
	}
	for _, edge := range readme.Edges {
		if edge.To == fileSymbolID(request.Repository.Identity, "not-a-link") {
			t.Fatal("fenced code was interpreted as a link")
		}
		if edge.Kind == graph.EdgeGenerates {
			t.Fatal("generated-from example inside a fence created provenance")
		}
		if strings.Contains(edge.To, "unsupported-template") || strings.Contains(edge.To, "unresolved-fragment") {
			t.Fatalf("unsupported or unresolved reference created a fake endpoint: %#v", edge)
		}
		if edge.To == fileSymbolID(request.Repository.Identity, "docs/Guide.md") {
			t.Fatal("case-mismatched local path was treated as resolved")
		}
	}

	generated := unitForPath(t, result.Batches, "articles/readme.md")
	assertEdgeFromTo(t, generated.Edges, graph.EdgeGenerates,
		fileSymbolID(request.Repository.Identity, "README.md"),
		fileSymbolID(request.Repository.Identity, "articles/readme.md"))
	for _, symbol := range generated.Symbols {
		if symbol.Evidence != graph.EvidenceGenerated {
			t.Fatalf("generated symbol evidence = %q, want generated", symbol.Evidence)
		}
	}

	inventory := unitByVariant(t, result.Batches, "inventory")
	assertSymbol(t, inventory.Symbols, "workspace", ".")
	assertSymbol(t, inventory.Symbols, "directory", "docs/")
	assertSymbol(t, inventory.Symbols, "topic", "topic:knowledge-graphs")
	assertSymbol(t, inventory.Symbols, "url", "url:https://example.com/reference?q=1#part")
	assertSymbol(t, inventory.Symbols, "url", "url:"+githubURL)
	assertEdgeFromTo(t, inventory.Edges, graph.EdgeResolvesTo, stableID("url", "url:"+githubURL), sectionSymbolID("github.com/TheFellow/other", "docs/design.md", "model"))
	branchURL := "https://github.com/TheFellow/other/blob/feature/foo/docs/design.md"
	rootFragmentURL := "https://github.com/TheFellow/other#readme"
	for _, edge := range inventory.Edges {
		if edge.Kind == graph.EdgeResolvesTo && edge.From == stableID("url", "url:"+branchURL) {
			t.Fatal("ambiguous slash-containing Git ref was mapped to the current checkout")
		}
		if edge.Kind == graph.EdgeResolvesTo && edge.From == stableID("url", "url:"+rootFragmentURL) {
			t.Fatal("GitHub repository fragment was discarded by a workspace alias")
		}
	}
	ignoredID := fileSymbolID(request.Repository.Identity, "ignored.md")
	for _, facts := range result.Batches {
		for _, symbol := range facts.Symbols {
			if symbol.ID == ignoredID {
				t.Fatal("Git-ignored path was indexed")
			}
		}
	}

	previous := manifest(result.Units)
	request.Previous = previous
	unchanged, err := provider.Refresh(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.Batches) != 0 || len(unchanged.Removed) != 0 {
		t.Fatalf("unchanged refresh batches=%d removed=%v", len(unchanged.Batches), unchanged.Removed)
	}

	file := filepath.Join(root, "README.md")
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(content, []byte("\nA prose-only edit.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := provider.Refresh(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Batches) != 1 || changed.Batches[0].Documents[0].Path != "README.md" {
		t.Fatalf("prose edit rebuilt unexpected units: %v", batchPaths(changed.Batches))
	}

	request.Previous = manifest(changed.Units)
	content, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "https://example.com/reference?q=1#part", "https://example.com/replacement", 1))
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	registryChange, err := provider.Refresh(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(batchPaths(registryChange.Batches), ","); got != "README.md,inventory" {
		t.Fatalf("URL registry edit rebuilt %q, want README.md,inventory", got)
	}

	request.Previous = manifest(registryChange.Units)
	if err := os.Remove(filepath.Join(root, "docs", "code.go")); err != nil {
		t.Fatal(err)
	}
	pathChange, err := provider.Refresh(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	updatedReadme := unitForPath(t, pathChange.Batches, "README.md")
	for _, edge := range updatedReadme.Edges {
		if edge.To == fileSymbolID(request.Repository.Identity, "docs/code.go") {
			t.Fatal("deleted non-Markdown target survived document re-resolution")
		}
	}
}

func TestProviderDoesNotFollowSymlinkedMarkdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("# Secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	git(t, root, "add", "linked.md")
	result, err := (Provider{}).Refresh(context.Background(), freshness.Request{Repository: repository.Repository{Root: root, Identity: "example/symlink"}})
	if err != nil {
		t.Fatal(err)
	}
	unit := unitForSymbol(t, result.Batches, fileSymbolID("example/symlink", "linked.md"))
	if unit.Symbols[0].Kind != "symlink" || len(unit.Documents) != 0 {
		t.Fatalf("symlink unit = %#v", unit)
	}
}

func TestBoundedReadRejectsPathIdentityChange(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.md")
	second := filepath.Join(root, "second.md")
	if err := os.WriteFile(first, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(second, expected, 1024); err == nil {
		t.Fatal("read accepted a different file identity")
	}
}

func TestHeadingAnchorHandlesEscapesUnicodeAndDuplicates(t *testing.T) {
	model, err := parseDocument("example/repo", "README.md", []byte("# 🧪 IEquatable\\<T\\>\n## Café API\n## Café API\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(model.headings))
	for _, heading := range model.headings {
		got = append(got, heading.anchor)
	}
	want := []string{"iequatablet", "café-api", "café-api-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("anchors = %v, want %v", got, want)
	}
}

func TestMarkdownSectionSearchTermsComeFromItsBody(t *testing.T) {
	content := []byte("# Benchmark\n\nRetained artifacts make correctness auditable.\n\n## Result\n\nA single paired sample is not a population estimate.\n")
	model, err := parseDocument("example/repo", "README.md", content)
	if err != nil {
		t.Fatal(err)
	}
	facts := model.facts(newResolver("example/repo", []entry{{path: "README.md", kind: "file"}}, map[string]*documentModel{"README.md": model}))
	benchmark := slices.IndexFunc(facts.Symbols, func(symbol graph.Symbol) bool { return symbol.StableName == "README.md#benchmark" })
	result := slices.IndexFunc(facts.Symbols, func(symbol graph.Symbol) bool { return symbol.StableName == "README.md#result" })
	if benchmark < 0 || result < 0 {
		t.Fatalf("section symbols = %#v", facts.Symbols)
	}
	if !slices.Contains(facts.Symbols[benchmark].SearchTerms, "artifacts") || slices.Contains(facts.Symbols[benchmark].SearchTerms, "population") {
		t.Fatalf("benchmark search terms = %q", facts.Symbols[benchmark].SearchTerms)
	}
	if !slices.Contains(facts.Symbols[result].SearchTerms, "population") || !slices.Contains(facts.Symbols[result].SearchTerms, "sample") {
		t.Fatalf("result search terms = %q", facts.Symbols[result].SearchTerms)
	}
}

func TestMalformedStructuredDocumentsDegradeToFileTopology(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"README.md":  []byte("# Good\n"),
		"invalid.md": {0xff, 0xfe},
		"broken.md":  []byte("---\ntitle: never closed\n# Body\n"),
		"huge.md":    bytes.Repeat([]byte{'x'}, maxDocumentBytes+1),
		"wide.md":    []byte("# " + strings.Repeat("h", maxExtractedText+1) + "\n"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	identity := "example/fallback"
	result, err := (Provider{}).Refresh(context.Background(), freshness.Request{Repository: repository.Repository{Root: root, Identity: identity}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid.md", "broken.md", "huge.md", "wide.md"} {
		facts := unitForSymbol(t, result.Batches, fileSymbolID(identity, name))
		if len(facts.Documents) != 0 || facts.Symbols[0].Kind != "file" {
			t.Fatalf("%s did not degrade to topology-only facts: %#v", name, facts)
		}
	}
	if len(unitForPath(t, result.Batches, "README.md").Documents) != 1 {
		t.Fatal("valid Markdown was not indexed alongside malformed documents")
	}
	if len(result.Diagnostics) != 4 {
		t.Fatalf("degradation diagnostics = %q, want one per malformed document", result.Diagnostics)
	}
}

func TestDuplicateMetadataEdgesAreCompactedAndAbsoluteRoutesRejected(t *testing.T) {
	content := []byte("---\npermalink: https://evil.example/x\nredirect_from: [/old/, /old/]\ntopics: [Go]\ntags: [go]\n---\n# Page\n")
	model, err := parseDocument("example/repo", "README.md", content)
	if err != nil {
		t.Fatal(err)
	}
	entries := []entry{{path: "README.md", kind: "file"}}
	resolver := newResolver("example/repo", entries, map[string]*documentModel{"README.md": model})
	facts := model.facts(resolver)
	if err := facts.Validate(); err != nil {
		t.Fatalf("duplicate metadata produced invalid facts: %v", err)
	}
	for _, registry := range resolver.registries() {
		if registry.kind == "route" && registry.stable == "/x/" {
			t.Fatal("absolute permalink was converted into a local route")
		}
	}
}

func TestGeneratedMarkersAwayFromTheDocumentPreambleAreInert(t *testing.T) {
	model, err := parseDocument("example/repo", "llms-full.txt", []byte("# Combined\n\n<!-- Generated from /one/ by tool -->\n<!-- Generated from /two/ by tool -->\n"))
	if err != nil {
		t.Fatal(err)
	}
	if model.generatedFrom != "" {
		t.Fatalf("embedded generated marker became document provenance: %q", model.generatedFrom)
	}
}

func TestLLMSAggregationsAreGeneratedEvidence(t *testing.T) {
	model, err := parseDocument("example/repo", "llms-full.txt", []byte("# Combined\n\nCollected content.\n"))
	if err != nil {
		t.Fatal(err)
	}
	facts := model.facts(newResolver("example/repo", []entry{{path: "llms-full.txt", kind: "file"}}, map[string]*documentModel{"llms-full.txt": model}))
	for _, symbol := range facts.Symbols {
		if symbol.Evidence != graph.EvidenceGenerated {
			t.Fatalf("LLM aggregation evidence = %q, want generated", symbol.Evidence)
		}
	}
}

func TestInventoryFactBoundIsDeterministic(t *testing.T) {
	entries := []entry{{path: "a/file.md", kind: "file"}, {path: "b/file.md", kind: "file"}}
	resolver := newResolver("example/repo", entries, nil)
	if _, err := buildInventoryBounded("example/repo", entries, nil, resolver, 4); err == nil {
		t.Fatal("incomplete inventory was published at the fact bound")
	}
	left, err := buildInventoryBounded("example/repo", entries, nil, resolver, 20)
	if err != nil {
		t.Fatal(err)
	}
	right, err := buildInventoryBounded("example/repo", entries, nil, resolver, 20)
	if err != nil {
		t.Fatal(err)
	}
	if factCount(left) > 20 || !reflect.DeepEqual(left, right) {
		t.Fatalf("bounded inventory is oversized or nondeterministic: left=%#v right=%#v", left, right)
	}
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", `---
title: Semantic Workspace
permalink: /readme/
redirect_from: /old-readme/
series: Design Notes
topics: [knowledge-graphs, tooling]
---
# Overview
[Guide](docs/guide.md) and [details](docs/guide.md#details).
[Missing fragment](docs/guide.md#absent).
[Case mismatch](docs/Guide.md).
[Other repo](https://github.com/TheFellow/other/blob/main/docs/design.md#model).
[Other branch](https://github.com/TheFellow/other/blob/feature/foo/docs/design.md).
[Repository fragment](https://github.com/TheFellow/other#readme).
[External](https://example.com/reference?q=1#part).
[](notes.txt) [](notes.txt)

<figure><img src="{{ '/assets/picture.png' | relative_url }}"></figure>

[![CI](assets/badge.svg)](https://example.com/actions)
<h2>HTML section</h2><a href="docs/code.go">code</a>
<a href="{{ page.url | relative_url }}">dynamic</a>

## API
## API

	`+"```mermaid\nA[not-a-link] --> B[still-not]\n<!-- Generated from /wrong/ by example -->\n```\n")
	write("docs/guide.md", "# Guide\n## Details\n")
	write("docs/code.go", "package docs\n")
	write("articles/readme.md", "<!-- Generated from /readme/ by scripts/generate.py; do not edit. -->\n# Generated\n")
	write("assets/picture.png", "not really a png")
	write("assets/badge.svg", "<svg></svg>")
	write("notes.txt", "plain file")
	write(".gitignore", "ignored.md\n")
	write("ignored.md", "# Ignored\n")
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	return root
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func manifest(units []freshness.Unit) *freshness.Manifest {
	return &freshness.Manifest{Units: append([]freshness.Unit(nil), units...)}
}

func unitForPath(t *testing.T, batches []graph.UnitFacts, name string) graph.UnitFacts {
	t.Helper()
	for _, facts := range batches {
		if len(facts.Documents) != 0 && facts.Documents[0].Path == name {
			return facts
		}
	}
	t.Fatalf("no unit for path %q", name)
	return graph.UnitFacts{}
}

func unitByVariant(t *testing.T, batches []graph.UnitFacts, variant string) graph.UnitFacts {
	t.Helper()
	for _, facts := range batches {
		if facts.Unit.Variant == variant {
			return facts
		}
	}
	t.Fatalf("no %s unit", variant)
	return graph.UnitFacts{}
}

func unitForSymbol(t *testing.T, batches []graph.UnitFacts, id string) graph.UnitFacts {
	t.Helper()
	for _, facts := range batches {
		for _, symbol := range facts.Symbols {
			if symbol.ID == id {
				return facts
			}
		}
	}
	t.Fatalf("no unit for symbol %s", id)
	return graph.UnitFacts{}
}

func assertSymbol(t *testing.T, symbols []graph.Symbol, kind, stable string) {
	t.Helper()
	for _, symbol := range symbols {
		if symbol.Kind == kind && symbol.StableName == stable {
			return
		}
	}
	t.Errorf("missing %s symbol %q", kind, stable)
}

func assertEdge(t *testing.T, edges []graph.Edge, kind graph.EdgeKind, target string) {
	t.Helper()
	for _, edge := range edges {
		if edge.Kind == kind && edge.To == target {
			return
		}
	}
	t.Errorf("missing %s edge to %s", kind, target)
}

func assertEdgeFromTo(t *testing.T, edges []graph.Edge, kind graph.EdgeKind, from, to string) {
	t.Helper()
	for _, edge := range edges {
		if edge.Kind == kind && edge.From == from && edge.To == to {
			return
		}
	}
	t.Errorf("missing %s edge %s -> %s", kind, from, to)
}

func batchPaths(batches []graph.UnitFacts) []string {
	var result []string
	for _, facts := range batches {
		if len(facts.Documents) != 0 {
			result = append(result, facts.Documents[0].Path)
		} else {
			result = append(result, facts.Unit.Variant)
		}
	}
	slices.Sort(result)
	return result
}
