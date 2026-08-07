package bridge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
)

func TestLoadStrictExactLinks(t *testing.T) {
	path := writeConfig(t, `{"schema":"weave.bridges/v1","links":[
{"id":"schema-generates-client","from":"symbol:scip proto api 1 User#","to":"symbol:scip go example client 1 User#","kind":"generates"},
	{"id":"guide-calls-api","from":"entity:workspace-section:guide","to":"symbol:scip go example api 1 Serve().","kind":"calls","note":"The guide's executable example invokes this API."}]}`)
	config, err := bridge.Load(path)
	if err != nil || len(config.Links) != 2 {
		t.Fatalf("Load() = %#v, %v", config, err)
	}
	if endpoint, err := bridge.Endpoint(config.Links[1].From); err != nil || endpoint != "workspace-section:guide" || config.Links[1].Note == "" {
		t.Fatalf("heterogeneous endpoint = %q, %v; link = %#v", endpoint, err, config.Links[1])
	}
	for _, input := range []string{
		`{"schema":"weave.bridges/v1","unknown":true,"links":[]}`,
		`{"schema":"weave.bridges/v1","links":[{"id":"x","from":"Serve","to":"symbol:y","kind":"depends-on"}]}`,
		`{"schema":"weave.bridges/v1","links":[{"id":"x","from":"entity:x","to":"entity:y","kind":"magic"}]}`,
		`{"schema":"weave.bridges/v1","links":[{"id":"x","from":"entity:x","to":"entity:y","kind":"links-to","note":"\u0000"}]}`,
	} {
		if _, err := bridge.Load(writeConfig(t, input)); err == nil {
			t.Fatalf("Load(%s) succeeded", input)
		}
	}
}

func TestSaveWritesCanonicalConfigurationAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".weave", "bridges.json")
	config := bridge.Config{Schema: bridge.Schema, Links: []bridge.Link{
		{ID: "z", From: bridge.Entity("url:https://example.test"), To: bridge.Entity("section:guide"), Kind: graph.EdgeLinksTo},
		{ID: "a", From: bridge.Entity("file:README.md"), To: bridge.Entity("section:guide"), Kind: graph.EdgeDocuments, Note: "why"},
	}}
	if err := bridge.Save(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err := bridge.Load(path)
	if err != nil || len(loaded.Links) != 2 || loaded.Links[0].ID != "a" || loaded.Links[1].ID != "z" {
		t.Fatalf("Load after Save = %#v, %v", loaded, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(content), "\n") || !strings.Contains(string(content), `"note": "why"`) {
		t.Fatalf("encoded configuration = %q, %v", content, err)
	}

	symlink := filepath.Join(directory, "bridges-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := bridge.Save(symlink, loaded); err == nil {
		t.Fatal("Save through symlink succeeded")
	}
}

func TestRevisionIsCanonicalAndDistinguishesEmptyFromChanged(t *testing.T) {
	t.Parallel()
	empty, err := bridge.Revision(bridge.Config{Schema: bridge.Schema})
	if err != nil || !strings.HasPrefix(empty, "sha256:") {
		t.Fatalf("empty Revision() = %q, %v", empty, err)
	}
	left := bridge.Config{Schema: bridge.Schema, Links: []bridge.Link{
		{ID: "b", From: bridge.Entity("b-from"), To: bridge.Entity("b-to"), Kind: graph.EdgeCalls},
		{ID: "a", From: bridge.Entity("a-from"), To: bridge.Entity("a-to"), Kind: graph.EdgeDocuments},
	}}
	right := bridge.Config{Schema: bridge.Schema, Links: []bridge.Link{left.Links[1], left.Links[0]}}
	leftRevision, err := bridge.Revision(left)
	if err != nil {
		t.Fatal(err)
	}
	rightRevision, err := bridge.Revision(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftRevision != rightRevision {
		t.Fatalf("order changed revision: %q != %q", leftRevision, rightRevision)
	}
	changed := right
	changed.Links = append([]bridge.Link(nil), right.Links...)
	changed.Links[0].Note = "new context"
	changedRevision, err := bridge.Revision(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedRevision == rightRevision || changedRevision == empty {
		t.Fatalf("changed revision was not distinct: empty=%q original=%q changed=%q", empty, rightRevision, changedRevision)
	}
}

func TestEditSerializesConcurrentSourceUpdates(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".weave", "bridges.json")
	lockPath := filepath.Join(directory, ".git", "weave", "links.lock")
	const count = 8
	errors := make(chan error, count)
	var wait sync.WaitGroup
	for i := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- bridge.Edit(context.Background(), path, lockPath, func(config *bridge.Config) error {
				config.Links = append(config.Links, bridge.Link{
					ID: fmt.Sprintf("link-%02d", i), From: bridge.Entity(fmt.Sprintf("from-%02d", i)),
					To: bridge.Entity(fmt.Sprintf("to-%02d", i)), Kind: graph.EdgeLinksTo,
				})
				return nil
			})
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	config, err := bridge.Load(path)
	if err != nil || len(config.Links) != count {
		t.Fatalf("serialized config = %#v, %v", config, err)
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
