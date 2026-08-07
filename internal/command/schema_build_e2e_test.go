package command_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/schemabuild"
)

func TestSchemaProviderFeedsContextAndDOTThroughCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command("git", "init", "--initial-branch=main", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	source := `openapi: 3.0.3
info: {title: Pets, version: 1.0.0}
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Pet"}}}}
components:
  schemas:
    Pet: {type: object}
`
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "openapi.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "weave@example.test"}, {"config", "user.name", "Weave Test"},
		{"add", "."}, {"commit", "-m", "fixture"},
	} {
		process := exec.Command("git", args...)
		process.Dir = root
		if output, err := process.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	manager := &freshness.Manager{Directory: root, Provider: schemabuild.Provider{}, Command: "test"}
	app := application.Local{Directory: root, Freshness: manager}
	run := func(arguments ...string) (string, string, error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		rootCommand := command.New(app, command.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr})
		err := rootCommand.Run(context.Background(), append([]string{"weave"}, arguments...))
		return stdout.String(), stderr.String(), err
	}
	if stdout, stderr, err := run("index"); err != nil || stdout != "" || !strings.Contains(stderr, "refreshed") {
		t.Fatalf("index stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err := run("context", "--json", "listPets")
	if err != nil || stderr != "" || !strings.Contains(stdout, `"path":"api/openapi.yaml"`) || !strings.Contains(stdout, "listPets") {
		t.Fatalf("context stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = run("graph", "--kind", "references", "listPets")
	if err != nil || stderr != "" || !strings.Contains(stdout, "digraph weave") || !strings.Contains(stdout, "listPets") || !strings.Contains(stdout, "Pet") {
		t.Fatalf("graph stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}
