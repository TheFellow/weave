package contextquery_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/federation"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
)

func TestContextReturnsExactCurrentSourceAndDirectRelationshipsDeterministically(t *testing.T) {
	ctx := context.Background()
	content := "package demo\nfunc Target() {\n\tHelper()\n}\nfunc Caller() { Target() }\n"
	root := gitRepository(t, map[string]string{"main.go": content})
	database := filepath.Join(t.TempDir(), "index.db")
	facts := sourceFacts("main.go", content)
	writeFacts(t, database, facts)
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	locator := fixedLocator(root)
	options := contextquery.Options{Scope: "local", Limit: 8, ContextLines: 0, MaxSourceBytes: 4096}

	result, err := contextquery.Build(ctx, db, "target-id", options, locator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Schema != contextquery.Schema || result.Focus.Symbol.ID != "target-id" || len(result.Focus.Repositories) != 1 {
		t.Fatalf("focus = %#v", result.Focus)
	}
	if len(result.Evidence) != 2 || result.Evidence[0].Role != "definition" || result.Evidence[1].Role != "reference" {
		t.Fatalf("evidence = %#v", result.Evidence)
	}
	definition := result.Evidence[0]
	if definition.Range.Start.Line != 1 || definition.Range.Start.Column != 5 || definition.Source.Status != contextquery.SourceCurrent {
		t.Fatalf("definition = %#v", definition)
	}
	if len(definition.Source.Lines) != 1 || definition.Source.Lines[0].Number != 2 || definition.Source.Lines[0].Text != "func Target() {" {
		t.Fatalf("definition source = %#v", definition.Source)
	}
	if got := result.Evidence[1].Source.Lines; len(got) != 1 || got[0].Number != 5 || got[0].Text != "func Caller() { Target() }" {
		t.Fatalf("reference source = %#v", got)
	}
	if len(result.Incoming) != 1 || result.Incoming[0].Edge.From != "caller-id" || result.Incoming[0].Entity == nil || result.Incoming[0].Entity.Symbol.DisplayName != "Caller" {
		t.Fatalf("incoming = %#v", result.Incoming)
	}
	if len(result.Outgoing) != 1 || result.Outgoing[0].Edge.To != "helper-id" || result.Outgoing[0].Entity == nil || result.Outgoing[0].Entity.Symbol.DisplayName != "Helper" {
		t.Fatalf("outgoing = %#v", result.Outgoing)
	}
	if result.Incoming[0].Edge.DocumentID != "" || len(result.Incoming[0].Repositories) != 1 || result.Incoming[0].Repositories[0].WorktreeID != "main" || result.Outgoing[0].Edge.DocumentID != "" || len(result.Outgoing[0].Repositories) != 1 {
		t.Fatalf("document-free relationship provenance = incoming %#v, outgoing %#v", result.Incoming[0], result.Outgoing[0])
	}
	if result.Metadata.SourceBytes == 0 || result.Metadata.Truncation != (contextquery.Truncation{}) {
		t.Fatalf("metadata = %#v", result.Metadata)
	}

	again, err := contextquery.Build(ctx, db, "target-id", options, locator)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(result)
	right, _ := json.Marshal(again)
	if string(left) != string(right) {
		t.Fatalf("context is nondeterministic:\n%s\n%s", left, right)
	}
}

func TestContextProjectsCurrentSourceForRelationshipEvidence(t *testing.T) {
	ctx := context.Background()
	content := "package demo\nfunc Target() { Helper() }\n"
	root := gitRepository(t, map[string]string{"main.go": content})
	facts := sourceFacts("main.go", content)
	facts.Edges[1].DocumentID = "document-id"
	facts.Edges[1].Range = graph.Range{
		Start: graph.Position{Line: 1, Column: 16, Byte: -1},
		End:   graph.Position{Line: 1, Column: 22, Byte: -1},
	}
	database := filepath.Join(t.TempDir(), "index.db")
	writeFacts(t, database, facts)
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	locatorCalls := map[string]int{}
	locator := func(kind, id string) []contextquery.Repository {
		locatorCalls[kind+"/"+id]++
		return []contextquery.Repository{{Identity: "example/repo", WorktreeID: "main", Root: root}}
	}
	result, err := contextquery.Build(ctx, db, "target-id", contextquery.Options{
		Scope: "local", Limit: 8, ContextLines: 0, MaxSourceBytes: 4096,
	}, locator)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outgoing) != 1 || result.Outgoing[0].Document == nil || result.Outgoing[0].Source == nil {
		t.Fatalf("relationship evidence = %#v", result.Outgoing)
	}
	if got := result.Outgoing[0].Source; got.Status != contextquery.SourceCurrent || got.Path != "main.go" || len(got.Lines) != 1 || got.Lines[0].Text != "func Target() { Helper() }" {
		t.Fatalf("relationship source = %#v", got)
	}
	if len(result.Outgoing[0].Repositories) != 1 || result.Metadata.SourceBytes == 0 {
		t.Fatalf("relationship provenance/metadata = %#v / %#v", result.Outgoing[0], result.Metadata)
	}
	if locatorCalls["edge/outgoing"] != 1 {
		t.Fatalf("outgoing edge provenance locator calls = %d, want 1", locatorCalls["edge/outgoing"])
	}
}

func TestContextReportsIndependentFactAndSourceTruncation(t *testing.T) {
	ctx := context.Background()
	content := "func Target() {}\nTarget()\nTarget()\n"
	root := gitRepository(t, map[string]string{"main.go": content})
	facts := sourceFacts("main.go", content)
	facts.Occurrences = append(facts.Occurrences,
		occurrence("reference-two", "target-id", "document-id", "reference", 2, 0, 6),
	)
	facts.Edges = append(facts.Edges,
		edge("incoming-two", "other-caller", "target-id", graph.EdgeReferences),
		edge("outgoing-two", "target-id", "other-helper", graph.EdgeDependsOn),
	)
	database := filepath.Join(t.TempDir(), "index.db")
	writeFacts(t, database, facts)
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := contextquery.Build(ctx, db, "target-id", contextquery.Options{
		Scope: "local", Limit: 1, ContextLines: 0, MaxSourceBytes: 1,
	}, fixedLocator(root))
	if err != nil {
		t.Fatal(err)
	}
	want := contextquery.Truncation{Occurrences: true, Incoming: true, Outgoing: true, Source: true}
	if result.Metadata.Truncation != want || len(result.Evidence) != 1 || len(result.Incoming) != 1 || len(result.Outgoing) != 1 {
		t.Fatalf("result bounds = %#v", result)
	}
	if result.Evidence[0].Source.Status != contextquery.SourceBudget || result.Metadata.SourceBytes != 0 {
		t.Fatalf("bounded source = %#v", result.Evidence[0].Source)
	}
}

func TestSymbolAnchorConsumesEvidenceLimitAndDisplacesReference(t *testing.T) {
	ctx := context.Background()
	content := "func Target() {}\nTarget()\n"
	root := gitRepository(t, map[string]string{"main.go": content})
	facts := sourceFacts("main.go", content)
	facts.Occurrences = []graph.Occurrence{occurrence("reference", "target-id", "document-id", "reference", 1, 0, 6)}
	facts.Symbols[0].Definition = graph.Range{
		Start: graph.Position{Line: 0, Column: 5, Byte: -1},
		End:   graph.Position{Line: 0, Column: 11, Byte: -1},
	}
	database := filepath.Join(t.TempDir(), "index.db")
	writeFacts(t, database, facts)
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := contextquery.Build(ctx, db, "target-id", contextquery.Options{
		Scope: "local", Limit: 1, ContextLines: 0, MaxSourceBytes: 4096,
	}, fixedLocator(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Kind != "anchor" || result.Evidence[0].Role != "definition" {
		t.Fatalf("evidence = %#v", result.Evidence)
	}
	if !result.Metadata.Truncation.Occurrences || result.Evidence[0].Source.Status != contextquery.SourceCurrent || result.Evidence[0].Source.Lines[0].Text != "func Target() {}" {
		t.Fatalf("anchor result = %#v", result)
	}
}

func TestContextRejectsAmbiguousTargets(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "index.db")
	writeFacts(t, database, graph.UnitFacts{
		Unit: graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Symbols: []graph.Symbol{
			symbol("a-id", "Target", "function", ""),
			symbol("b-id", "Target", "section", ""),
		},
	})
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = contextquery.Build(ctx, db, "Target", contextquery.Options{Scope: "local", Limit: 8, MaxSourceBytes: 1024}, nil)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "a-id") || !strings.Contains(err.Error(), "b-id") {
		t.Fatalf("ambiguous error = %v", err)
	}
}

func TestContextNeverReadsUnsafeOrMissingSource(t *testing.T) {
	ctx := context.Background()
	t.Run("out of root", func(t *testing.T) {
		root := gitRepository(t, map[string]string{"README.md": "safe\n"})
		outside := filepath.Join(root, "..", "outside.txt")
		if err := os.WriteFile(outside, []byte("do not expose\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := buildOneDocument(t, ctx, root, "../outside.txt", "", "malicious-id")
		if got := result.Evidence[0].Source; got.Status != contextquery.SourceUnsafePath || len(got.Lines) != 0 {
			t.Fatalf("unsafe source = %#v", got)
		}
	})
	t.Run("missing current file", func(t *testing.T) {
		content := "# Removed\n"
		root := gitRepository(t, map[string]string{"removed.md": content})
		if err := os.Remove(filepath.Join(root, "removed.md")); err != nil {
			t.Fatal(err)
		}
		result := buildOneDocument(t, ctx, root, "removed.md", contentHash(content), "missing-id")
		if got := result.Evidence[0].Source; got.Status != contextquery.SourceMissing || len(got.Lines) != 0 {
			t.Fatalf("missing source = %#v", got)
		}
	})
	t.Run("changed after indexing", func(t *testing.T) {
		indexed := "# Indexed\n"
		root := gitRepository(t, map[string]string{"changed.md": indexed})
		if err := os.WriteFile(filepath.Join(root, "changed.md"), []byte("# Current and different\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := buildOneDocument(t, ctx, root, "changed.md", contentHash(indexed), "changed-id")
		if got := result.Evidence[0].Source; got.Status != contextquery.SourceChanged || len(got.Lines) != 0 || got.Hash == "" {
			t.Fatalf("changed source = %#v", got)
		}
	})
	t.Run("intermediate directory symlink escape", func(t *testing.T) {
		indexed := "package safe\n"
		root := gitRepository(t, map[string]string{"nested/source.go": indexed})
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "source.go"), []byte("outside secret\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(root, "nested")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
			t.Skipf("directory symlinks are unavailable: %v", err)
		}
		result := buildOneDocument(t, ctx, root, "nested/source.go", contentHash(indexed), "escape-id")
		if got := result.Evidence[0].Source; got.Status != contextquery.SourceUnsafePath || len(got.Lines) != 0 || strings.Contains(got.Detail, "outside secret") {
			t.Fatalf("symlink escape source = %#v", got)
		}
	})
}

func TestContextReadsGitVisibleWorkspaceFileWithoutInventingDocumentFact(t *testing.T) {
	ctx := context.Background()
	content := "# Guide\n\nCurrent prose.\n"
	root := gitRepository(t, map[string]string{"README.md": content})
	database := filepath.Join(t.TempDir(), "index.db")
	writeFacts(t, database, graph.UnitFacts{
		Unit: graph.Unit{ID: "unit", Provider: "weave-workspace", ProviderVersion: "1"},
		Symbols: []graph.Symbol{{
			ID: "file-id", UnitID: "unit", StableName: "README.md", DisplayName: "README.md", Kind: "file",
			Provider: "weave-workspace", Evidence: graph.EvidenceExact,
		}},
	})
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := contextquery.Build(ctx, db, "file-id", contextquery.Options{
		Scope: "local", Limit: 8, ContextLines: 2, MaxSourceBytes: 4096,
	}, fixedLocator(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Kind != "entity" || result.Evidence[0].Document != nil {
		t.Fatalf("file evidence = %#v", result.Evidence)
	}
	source := result.Evidence[0].Source
	if source.Status != contextquery.SourceCurrent || source.Path != "README.md" || len(source.Lines) != 3 || source.Lines[0].Text != "# Guide" {
		t.Fatalf("file source = %#v", source)
	}
}

func TestContextHandlesGeneratedAndExternalEntitiesHonestly(t *testing.T) {
	ctx := context.Background()
	t.Run("generated document", func(t *testing.T) {
		content := "// generated\nfunc Client() {}\n"
		root := gitRepository(t, map[string]string{"client.gen.go": content})
		facts := oneDocumentFacts("generated-id", "client.gen.go", contentHash(content))
		facts.Symbols[0].Evidence = graph.EvidenceGenerated
		database := filepath.Join(t.TempDir(), "index.db")
		writeFacts(t, database, facts)
		db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		result, err := contextquery.Build(ctx, db, "generated-id", contextquery.Options{Scope: "local", Limit: 8, ContextLines: 1, MaxSourceBytes: 4096}, fixedLocator(root))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Evidence) != 1 || result.Evidence[0].Confidence != graph.EvidenceGenerated || result.Evidence[0].Source.Status != contextquery.SourceCurrent {
			t.Fatalf("generated evidence = %#v", result.Evidence)
		}
	})
	t.Run("external entity without document", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "index.db")
		writeFacts(t, database, graph.UnitFacts{
			Unit:    graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
			Symbols: []graph.Symbol{symbol("external-id", "External", "function", "")},
		})
		db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		result, err := contextquery.Build(ctx, db, "external-id", contextquery.Options{Scope: "local", Limit: 8, ContextLines: 1, MaxSourceBytes: 4096}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Evidence) != 0 || result.Metadata.SourceBytes != 0 {
			t.Fatalf("external context = %#v", result)
		}
	})
}

func TestCatalogContextUsesOwningRepositoryProvenance(t *testing.T) {
	ctx := context.Background()
	content := "func Shared() {}\n"
	root := gitRepository(t, map[string]string{"shared.go": content})
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	catalogDB, err := catalog.Open(ctx, catalogPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := catalogDB.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogDB.Close(); err != nil {
		t.Fatal(err)
	}
	facts := oneDocumentFacts("shared-id", "shared.go", contentHash(content))
	writeFacts(t, entry.DatabasePath, facts)
	store, err := federation.Open(ctx, catalogPath, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locator := func(kind, id string) []contextquery.Repository {
		var result []contextquery.Repository
		for _, source := range store.SourcesFor(kind, id) {
			result = append(result, contextquery.Repository{Identity: source.Repository, WorktreeID: source.WorktreeID, Root: source.Root})
		}
		return result
	}
	result, err := contextquery.Build(ctx, store, "shared-id", contextquery.Options{
		Scope: "catalog", Limit: 8, ContextLines: 0, MaxSourceBytes: 4096,
	}, locator)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Focus.Repositories) != 1 || result.Focus.Repositories[0].Identity != entry.Identity || result.Focus.Repositories[0].Root != entry.Root {
		t.Fatalf("focus provenance = %#v, entry = %#v", result.Focus.Repositories, entry)
	}
	if len(result.Evidence) != 1 || len(result.Evidence[0].Repositories) != 1 || result.Evidence[0].Repositories[0].Root != entry.Root || result.Evidence[0].Source.Status != contextquery.SourceCurrent {
		t.Fatalf("evidence provenance = %#v", result.Evidence)
	}
}

func buildOneDocument(t *testing.T, ctx context.Context, root, name, hash, id string) contextquery.Result {
	t.Helper()
	database := filepath.Join(t.TempDir(), "index.db")
	writeFacts(t, database, oneDocumentFacts(id, name, hash))
	db, err := storage.Open(ctx, database, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := contextquery.Build(ctx, db, id, contextquery.Options{Scope: "local", Limit: 8, ContextLines: 0, MaxSourceBytes: 4096}, fixedLocator(root))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func oneDocumentFacts(id, name, hash string) graph.UnitFacts {
	document := graph.Document{ID: "document-id", UnitID: "unit", Path: name, Language: "text", ContentHash: hash, Provider: "fixture", ProviderVersion: "1"}
	return graph.UnitFacts{
		Unit:      graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Documents: []graph.Document{document},
		Symbols:   []graph.Symbol{symbol(id, id, "document", document.ID)},
	}
}

func sourceFacts(name, content string) graph.UnitFacts {
	facts := graph.UnitFacts{
		Unit: graph.Unit{ID: "unit", Provider: "fixture", ProviderVersion: "1"},
		Documents: []graph.Document{{
			ID: "document-id", UnitID: "unit", Path: name, Language: "go", ContentHash: contentHash(content), Provider: "fixture", ProviderVersion: "1",
		}},
		Symbols: []graph.Symbol{
			symbol("target-id", "Target", "function", "document-id"),
			symbol("caller-id", "Caller", "function", "document-id"),
			symbol("helper-id", "Helper", "function", "document-id"),
		},
		Occurrences: []graph.Occurrence{
			occurrence("definition", "target-id", "document-id", "definition", 1, 5, 11),
			occurrence("reference", "target-id", "document-id", "reference", 4, 16, 22),
		},
		Edges: []graph.Edge{
			edge("incoming", "caller-id", "target-id", graph.EdgeCalls),
			edge("outgoing", "target-id", "helper-id", graph.EdgeCalls),
		},
	}
	facts.Symbols[0].Definition = facts.Occurrences[0].Range
	return facts
}

func symbol(id, name, kind, document string) graph.Symbol {
	return graph.Symbol{
		ID: id, UnitID: "unit", StableName: "fixture." + name, DisplayName: name,
		Kind: kind, DocumentID: document, Provider: "fixture", Evidence: graph.EvidenceExact,
	}
}

func occurrence(id, symbolID, documentID, role string, line, start, end int32) graph.Occurrence {
	return graph.Occurrence{
		ID: id, UnitID: "unit", SymbolID: symbolID, DocumentID: documentID, Role: role,
		Range:    graph.Range{Start: graph.Position{Line: line, Column: start, Byte: -1}, End: graph.Position{Line: line, Column: end, Byte: -1}},
		Provider: "fixture", Evidence: graph.EvidenceExact,
	}
}

func edge(id, from, to string, kind graph.EdgeKind) graph.Edge {
	return graph.Edge{ID: id, UnitID: "unit", From: from, To: to, Kind: kind, Provider: "fixture", Evidence: graph.EvidenceExact}
}

func fixedLocator(root string) contextquery.Locator {
	return func(_, _ string) []contextquery.Repository {
		return []contextquery.Repository{{Identity: "example/repo", WorktreeID: "main", Root: root}}
	}
}

func contentHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func writeFacts(t *testing.T, database string, facts graph.UnitFacts) {
	t.Helper()
	db, err := storage.Open(context.Background(), database, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUnit(context.Background(), facts); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func gitRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "weave@example.test"}, {"config", "user.name", "Weave Test"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "fixture"}, {"remote", "add", "origin", "https://github.com/example/context-fixture.git"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}
