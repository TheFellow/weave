package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/catalog"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
)

func TestCatalogSymbolCLIEndToEndMaterializesAndReusesAggregate(t *testing.T) {
	ctx := context.Background()
	firstRoot := commandRepository(t)
	secondRoot := commandRepository(t)
	for index, root := range []string{firstRoot, secondRoot} {
		cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/acme/aggregate-"+string(rune('a'+index))+".git")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("add remote: %v: %s", err, output)
		}
	}
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	aggregateDirectory := filepath.Join(t.TempDir(), "aggregate")
	t.Setenv("WEAVE_AGGREGATE", aggregateDirectory)
	catalogDB, err := catalog.Open(ctx, catalogPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var registered []catalog.Entry
	for _, root := range []string{firstRoot, secondRoot} {
		entry, err := catalogDB.Add(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		registered = append(registered, entry)
	}
	if err := catalogDB.Close(); err != nil {
		t.Fatal(err)
	}
	managers := map[string]*freshness.Manager{}
	for _, entry := range registered {
		managers[filepath.Clean(entry.Root)] = &freshness.Manager{Directory: entry.Root, Provider: &aggregateCommandProvider{}, Command: "test"}
	}
	app := application.Local{
		CatalogPath: catalogPath,
		FreshnessFor: func(root string) *freshness.Manager {
			return managers[filepath.Clean(root)]
		},
	}
	run := func() (string, string, error) {
		var stdout, stderr bytes.Buffer
		root := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		err := root.Run(ctx, []string{"weave", "symbols", "HandleRequest", "--scope", "catalog", "--catalog", catalogPath, "--json"})
		return stdout.String(), stderr.String(), err
	}
	first, stderr, err := run()
	if err != nil || strings.Count(stderr, "aggregate fixture warning") != len(registered) {
		t.Fatalf("first query stdout=%q stderr=%q err=%v", first, stderr, err)
	}
	var response application.Response
	if err := json.Unmarshal([]byte(first), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Symbols) != 1 || response.Symbols[0].ID != "fixture:handle" || len(response.Sources) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Diagnostics) != len(registered) {
		t.Fatalf("provider diagnostics were not de-duplicated: %q", response.Diagnostics)
	}
	entries, err := os.ReadDir(aggregateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	databaseCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "graph-") && strings.HasSuffix(entry.Name(), ".db") {
			databaseCount++
		}
	}
	if databaseCount != 1 {
		t.Fatalf("aggregate entries = %#v", entries)
	}
	second, secondStderr, err := run()
	if err != nil || secondStderr != stderr || second != first {
		t.Fatalf("cache hit stdout=%q want=%q stderr=%q want_stderr=%q err=%v", second, first, secondStderr, stderr, err)
	}
}

type aggregateCommandProvider struct{ calls int }

func (*aggregateCommandProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "aggregate-command-fixture", Version: "1"}
}

func (p *aggregateCommandProvider) Refresh(context.Context, freshness.Request) (freshness.Result, error) {
	p.calls++
	return freshness.Result{
		Batches:     []graph.UnitFacts{commandFixture()},
		Units:       []freshness.Unit{{ID: "fixture", InventoryDigest: "fixture-v1"}},
		Diagnostics: []string{"aggregate fixture warning"},
	}, nil
}
