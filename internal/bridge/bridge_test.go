package bridge_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
)

func TestLoadStrictExactLinks(t *testing.T) {
	path := writeConfig(t, `{"schema":"weave.bridges/v1","links":[
{"id":"schema-generates-client","from":"symbol:scip proto api 1 User#","to":"symbol:scip go example client 1 User#","kind":"generates"},
{"id":"guide-documents-api","from":"symbol:scip markdown docs 1 Guide#","to":"symbol:scip go example api 1 Serve().","kind":"documents"}]}`)
	config, err := bridge.Load(path)
	if err != nil || len(config.Links) != 2 {
		t.Fatalf("Load() = %#v, %v", config, err)
	}
	for _, input := range []string{
		`{"schema":"weave.bridges/v1","unknown":true,"links":[]}`,
		`{"schema":"weave.bridges/v1","links":[{"id":"x","from":"Serve","to":"symbol:y","kind":"depends-on"}]}`,
		`{"schema":"weave.bridges/v1","links":[{"id":"x","from":"symbol:x","to":"symbol:y","kind":"calls"}]}`,
	} {
		if _, err := bridge.Load(writeConfig(t, input)); err == nil {
			t.Fatalf("Load(%s) succeeded", input)
		}
	}
}

func TestProviderNormalizesEvidenceAndRefreshesDeterministically(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".weave"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".weave", "bridges.json")
	if err := os.WriteFile(path, []byte(`{"schema":"weave.bridges/v1","links":[
{"id":"dependency","from":"symbol:caller","to":"symbol:target","kind":"depends-on"},
{"id":"generation","from":"symbol:schema","to":"symbol:client","kind":"generates"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := repository.Repository{Root: root, Identity: "https://example.test/org/repo"}
	first, err := (bridge.Provider{}).Refresh(context.Background(), freshness.Request{Repository: repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Batches) != 1 || len(first.Batches[0].Edges) != 2 {
		t.Fatalf("result = %#v", first)
	}
	edges := first.Batches[0].Edges
	if edges[0].From != "caller" || edges[0].Evidence != graph.EvidenceDeclared || edges[1].From != "schema" || edges[1].Evidence != graph.EvidenceGenerated {
		t.Fatalf("edges = %#v", edges)
	}
	manifest := &freshness.Manifest{Units: first.Units}
	second, err := (bridge.Provider{}).Refresh(context.Background(), freshness.Request{Repository: repo, Previous: manifest})
	if err != nil || len(second.Batches) != 0 || !slices.Equal(second.Units, first.Units) {
		t.Fatalf("unchanged refresh = %#v, %v", second, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removed, err := (bridge.Provider{}).Refresh(context.Background(), freshness.Request{Repository: repo, Previous: manifest})
	if err != nil || !slices.Equal(removed.Removed, []string{first.Units[0].ID}) {
		t.Fatalf("removed refresh = %#v, %v", removed, err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridges.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
