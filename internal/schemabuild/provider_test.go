package schemabuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
)

func TestProviderIndexesEverySupportedCategory(t *testing.T) {
	root := fixtureRepository(t, completeFixture())
	result := refreshFixture(t, root, nil, true)
	if len(result.Batches) != 6 || len(result.Units) != 6 {
		t.Fatalf("categories: batches=%d units=%d diagnostics=%v", len(result.Batches), len(result.Units), result.Diagnostics)
	}
	all := combineFacts(result.Batches)
	for _, kind := range []string{
		"protobuf-message", "protobuf-service", "protobuf-rpc", "protobuf-field",
		"openapi-operation", "openapi-schema", "graphql-interface", "graphql-object", "graphql-fragment",
		"sql-migration", "sql-table", "sql-column", "sql-view",
		"terraform-resource", "terraform-module", "terraform-output",
		"build-project-go", "build-project-cargo", "build-project-npm", "build-project-maven", "build-project-msbuild", "build-target",
	} {
		if !slices.ContainsFunc(all.Symbols, func(symbol graph.Symbol) bool { return symbol.Kind == kind }) {
			t.Errorf("missing symbol kind %q", kind)
		}
	}
	for _, edge := range []struct {
		kind     graph.EdgeKind
		evidence graph.Evidence
	}{
		{graph.EdgeImports, graph.EvidenceDeclared}, {graph.EdgeImplements, graph.EvidenceDeclared},
		{graph.EdgeDependsOn, graph.EvidenceDeclared}, {graph.EdgeReferences, graph.EvidenceDeclared},
		{graph.EdgeGenerates, graph.EvidenceGenerated},
	} {
		if !slices.ContainsFunc(all.Edges, func(candidate graph.Edge) bool {
			return candidate.Kind == edge.kind && candidate.Evidence == edge.evidence
		}) {
			t.Errorf("missing %s/%s relationship", edge.kind, edge.evidence)
		}
	}
	if !slices.ContainsFunc(all.Edges, func(edge graph.Edge) bool {
		return edge.Kind == graph.EdgeDependsOn && edge.Evidence == graph.EvidenceInferred &&
			strings.Contains(symbolName(all.Symbols, edge.From), "002__view.sql") &&
			strings.Contains(symbolName(all.Symbols, edge.To), "001__pets.sql")
	}) {
		t.Fatal("per-directory migration filename order was not retained as inferred evidence")
	}
	for _, symbol := range all.Symbols {
		if symbol.DocumentID != "" && symbol.Kind != "sql-migration" && symbol.Definition.Start.Byte < 0 && symbol.Definition.Start.Line == 0 && symbol.Definition.Start.Column == 0 {
			// Protobuf retains exact line/column ranges while some parsers retain bytes.
			t.Errorf("symbol %q has no source anchor: %#v", symbol.StableName, symbol.Definition)
		}
		if symbol.Evidence == graph.EvidenceExact {
			t.Errorf("source-only provider claimed compiler-exact evidence for %q", symbol.StableName)
		}
	}
	if !slices.ContainsFunc(all.Edges, func(edge graph.Edge) bool {
		return edge.Kind == graph.EdgeReferences && strings.Contains(symbolName(all.Symbols, edge.To), "Pet")
	}) {
		t.Fatal("schema operation/type relationships were not retained")
	}
}

func TestProviderIsDeterministicAndCategoryAtomic(t *testing.T) {
	files := map[string]string{
		"schema/types.proto": `syntax = "proto3"; package fixture; message Pet { string name = 1; }`,
		"spec/openapi.yaml": `openapi: 3.0.3
info: {title: Fixture, version: 1.0.0}
paths: {}
components:
  schemas:
    Pet: {type: object}
`,
	}
	root := fixtureRepository(t, files)
	first := refreshFixture(t, root, nil, true)
	second := refreshFixture(t, root, nil, true)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if !reflect.DeepEqual(firstJSON, secondJSON) {
		t.Fatal("forced rebuild is not deterministic")
	}
	previous := fixtureManifest(first.Units)
	writeFixture(t, root, "spec/openapi.yaml", "openapi: [")
	gitAdd(t, root)
	broken := refreshFixture(t, root, previous, false)
	protoID := stableID("unit", "example.test/repository", "protobuf")
	openAPIID := stableID("unit", "example.test/repository", "openapi")
	if !slices.ContainsFunc(broken.Units, func(unit freshness.Unit) bool { return unit.ID == protoID }) || !slices.Contains(broken.Removed, openAPIID) {
		t.Fatalf("category atomicity failed: units=%v removed=%v diagnostics=%v", broken.Units, broken.Removed, broken.Diagnostics)
	}
	if !slices.ContainsFunc(broken.Diagnostics, func(value string) bool { return strings.Contains(value, "openapi category omitted atomically") }) {
		t.Fatalf("missing bounded malformed-category diagnostic: %v", broken.Diagnostics)
	}
	if len(broken.Batches) != 0 {
		t.Fatalf("unchanged protobuf should have reused its cached unit, batches=%d", len(broken.Batches))
	}

	if err := os.Remove(filepath.Join(root, "schema", "types.proto")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "spec/openapi.yaml", files["spec/openapi.yaml"])
	gitAdd(t, root)
	repaired := refreshFixture(t, root, fixtureManifest(broken.Units), false)
	if !slices.Contains(repaired.Removed, protoID) || !slices.ContainsFunc(repaired.Batches, func(facts graph.UnitFacts) bool { return facts.Unit.ID == openAPIID }) {
		t.Fatalf("incremental delete/repair failed: batches=%v removed=%v", repaired.Batches, repaired.Removed)
	}
}

func TestOpenAPIRefsStayLocalOrOpenWithoutReadingOutsideRepository(t *testing.T) {
	root := fixtureRepository(t, map[string]string{
		"spec/openapi.yaml": `openapi: 3.1.0
info: {title: Safe, version: 1.0.0}
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "./openapi-components.yaml#/components/schemas/Pet"}
  /remote:
    get:
      operationId: remote
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "https://example.invalid/schema.yaml#/Pet"}}}}
  /escape:
    get:
      operationId: escape
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "../../../secret.yaml#/Pet"}}}}
  /missing:
    get:
      operationId: missing
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "./openapi-components.yaml#/components/schemas/Missing"}}}}
`,
		"spec/openapi-components.yaml": `components: {schemas: {Pet: {type: object}}}`,
	})
	secret := filepath.Join(filepath.Dir(root), "secret.yaml")
	if err := os.WriteFile(secret, []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := refreshFixture(t, root, nil, true)
	if len(result.Batches) != 1 {
		t.Fatalf("OpenAPI root disappeared because of an external ref: batches=%d diagnostics=%v", len(result.Batches), result.Diagnostics)
	}
	if !slices.ContainsFunc(result.Batches[0].Symbols, func(symbol graph.Symbol) bool {
		return symbol.StableName == "spec/openapi-components.yaml#/components/schemas/Pet"
	}) {
		t.Fatal("contained Git-visible external file ref was not resolved")
	}
	for _, fragment := range []string{"remote $ref left unresolved", "escaping local $ref left unresolved", "local $ref fragment was not found"} {
		if !slices.ContainsFunc(result.Diagnostics, func(value string) bool { return strings.Contains(value, fragment) }) {
			t.Errorf("missing diagnostic %q in %v", fragment, result.Diagnostics)
		}
	}
}

func TestUnsupportedPostgreSQLStatementIsHonest(t *testing.T) {
	root := fixtureRepository(t, map[string]string{
		"db/migrations/001__schema.sql": "CREATE TABLE pets (id bigint PRIMARY KEY);\nGRANT SELECT ON pets TO reader;\n",
	})
	result := refreshFixture(t, root, nil, true)
	if len(result.Batches) != 1 || !slices.ContainsFunc(result.Batches[0].Symbols, func(symbol graph.Symbol) bool { return symbol.Kind == "sql-table" }) {
		t.Fatalf("supported PostgreSQL DDL missing: %#v", result)
	}
	if !slices.ContainsFunc(result.Diagnostics, func(value string) bool { return strings.Contains(value, "parsed but not semantically modeled") }) {
		t.Fatalf("unsupported statement was silently invented or discarded: %v", result.Diagnostics)
	}
	previousDiagnostics := []string{providerName + ": " + result.Diagnostics[0], "weave-workspace: unrelated diagnostic"}
	cached := refreshFixture(t, root, &freshness.Manifest{Provider: (Provider{}).ID(), Units: result.Units, Diagnostics: previousDiagnostics}, false)
	if len(cached.Batches) != 0 || !slices.ContainsFunc(cached.Diagnostics, func(value string) bool { return strings.Contains(value, "parsed but not semantically modeled") }) {
		t.Fatalf("cached category lost its persistent diagnostic: %#v", cached)
	}
}

func TestMalformedCategoryNeverPublishesPartialFacts(t *testing.T) {
	tests := []struct {
		name, path, content, category string
	}{
		{"protobuf", "broken.proto", `syntax = "proto3"; message {`, "protobuf"},
		{"openapi", "openapi.yaml", "openapi: [", "openapi"},
		{"graphql", "broken.graphql", "type Query {", "graphql"},
		{"sql", "db/migrations/001__broken.sql", "CREATE TABLE (", "sql-migrations"},
		{"terraform", "main.tf", `resource "thing" {`, "terraform"},
		{"build", "package.json", `{"name":`, "build"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := fixtureRepository(t, map[string]string{test.path: test.content})
			result := refreshFixture(t, root, nil, true)
			if len(result.Batches) != 0 || len(result.Units) != 0 {
				t.Fatalf("malformed category published partial facts: %#v", result)
			}
			if !slices.ContainsFunc(result.Diagnostics, func(value string) bool {
				return strings.Contains(value, test.category+" category omitted atomically")
			}) {
				t.Fatalf("missing atomic diagnostic: %v", result.Diagnostics)
			}
		})
	}
}

func TestMalformedBuildManifestDoesNotHideIndependentProjects(t *testing.T) {
	root := fixtureRepository(t, map[string]string{
		"go.mod":                 "module example.test/valid\ngo 1.26\n",
		"fixtures/Broken.csproj": `<Project><ItemGroup>`,
	})
	result := refreshFixture(t, root, nil, true)
	if len(result.Batches) != 1 || !slices.ContainsFunc(result.Batches[0].Symbols, func(symbol graph.Symbol) bool {
		return symbol.Kind == "build-project-go" && symbol.DisplayName == "example.test/valid"
	}) {
		t.Fatalf("malformed independent manifest hid valid build facts: %#v", result)
	}
	if !slices.ContainsFunc(result.Diagnostics, func(value string) bool {
		return strings.Contains(value, "build manifest fixtures/Broken.csproj omitted")
	}) {
		t.Fatalf("malformed manifest was not diagnosed: %v", result.Diagnostics)
	}
}

func TestProviderHonorsCanceledRefresh(t *testing.T) {
	root := fixtureRepository(t, map[string]string{
		"schema.proto": `syntax = "proto3"; message Pet {}`,
	})
	repo, err := repository.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (Provider{}).Refresh(ctx, freshness.Request{Repository: repo})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh error = %v, want context.Canceled", err)
	}
}

func TestProviderSkipsGitVisibleSymlinkInputs(t *testing.T) {
	root := fixtureRepository(t, map[string]string{
		"schema.proto": `syntax = "proto3"; message Pet {}`,
	})
	outside := filepath.Join(t.TempDir(), "openapi.yaml")
	if err := os.WriteFile(outside, []byte(`openapi: 3.0.3
info: {title: Must Not Be Read, version: 1.0.0}
paths: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "openapi.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	gitAdd(t, root)
	result := refreshFixture(t, root, nil, true)
	if len(result.Batches) != 1 || classify(result.Batches[0].Documents[0].Path) != "protobuf" {
		t.Fatalf("symlink was followed or regular source disappeared: %#v", result)
	}
}

func TestProviderRejectsOversizedSource(t *testing.T) {
	root := fixtureRepository(t, nil)
	full := filepath.Join(root, "schema.proto")
	data := bytes.Repeat([]byte{' '}, maxSourceBytes+1)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	gitAdd(t, root)
	repo, err := repository.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Provider{}).Refresh(context.Background(), freshness.Request{Repository: repo})
	if err == nil || !strings.Contains(err.Error(), "source exceeds") {
		t.Fatalf("Refresh error = %v, want bounded source error", err)
	}
}

func completeFixture() map[string]string {
	return map[string]string{
		"proto/types.proto": `syntax = "proto3";
package fixture.v1;
message Pet { string name = 1; }
enum State { STATE_UNSPECIFIED = 0; STATE_READY = 1; }
`,
		"proto/api.proto": `syntax = "proto3";
package fixture.v1;
import "proto/types.proto";
service Pets { rpc Get(Pet) returns (Pet); }
`,
		"api/openapi.yaml": `openapi: 3.0.3
info: {title: Pets API, version: 1.0.0}
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Pet"}}}}
components:
  schemas:
    Pet:
      type: object
      properties:
        name: {type: string}
`,
		"graphql/schema.graphql": `interface Node { id: ID! }
type Pet implements Node { id: ID!, name: String! }
type Query { pet: Pet }
`,
		"graphql/query.graphql": `query GetPet { pet { ...PetFields } }
fragment PetFields on Pet { name }
`,
		"db/migrations/001__pets.sql": `CREATE TABLE owners (id bigint PRIMARY KEY);
CREATE TABLE pets (id bigint PRIMARY KEY, owner_id bigint REFERENCES owners(id), name text);
`,
		"db/migrations/002__view.sql": `CREATE VIEW named_pets AS SELECT name FROM pets;`,
		"infra/main.tf": `terraform {
  required_providers { random = { source = "hashicorp/random", version = "~> 3.0" } }
}
resource "random_pet" "name" {}
module "child" {
  source = "./modules/child"
  depends_on = [random_pet.name]
}
output "pet_name" { value = random_pet.name.id }
`,
		"infra/modules/child/main.tf": `variable "prefix" { type = string }
locals { value = var.prefix }
`,
		"go.mod": `module example.test/root
go 1.26
require example.test/local v0.0.0
replace example.test/local => ./local
`,
		"local/go.mod": `module example.test/local
go 1.26
`,
		"Cargo.toml": `[package]
name = "fixture-rust"
version = "0.1.0"
[dependencies]
util = { path = "crates/util" }
[[bin]]
name = "fixture-cli"
`,
		"crates/util/Cargo.toml": `[package]
name = "util"
version = "0.1.0"
`,
		"package.json":             `{"name":"fixture-js","scripts":{"generate":"node generate.js"},"workspaces":{"packages":["packages/ui"]},"dependencies":{"ui":"file:packages/ui"}}`,
		"packages/ui/package.json": `{"name":"ui"}`,
		"pom.xml":                  `<project><groupId>example.test</groupId><artifactId>fixture-jvm</artifactId><modules><module>jvm-child</module></modules></project>`,
		"jvm-child/pom.xml":        `<project><parent><groupId>example.test</groupId><artifactId>fixture-jvm</artifactId><relativePath>../pom.xml</relativePath></parent><artifactId>child</artifactId></project>`,
		"src/App.csproj":           `<Project><PropertyGroup><AssemblyName>Fixture.DotNet</AssemblyName></PropertyGroup><ItemGroup><ProjectReference Include="../lib/Lib.csproj"/><Compile Update="Generated/Pet.g.cs"><AutoGen>true</AutoGen><DependentUpon>Pet.proto</DependentUpon></Compile></ItemGroup><Target Name="Generate"/><Target Name="Pack" DependsOnTargets="Generate;SdkImported"/></Project>`,
		"lib/Lib.csproj":           `<Project><PropertyGroup><AssemblyName>Fixture.Lib</AssemblyName></PropertyGroup></Project>`,
	}
}

func fixtureRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command("git", "init", "--initial-branch=main", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for name, content := range files {
		writeFixture(t, root, name, content)
	}
	gitAdd(t, root)
	return root
}

func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitAdd(t *testing.T, root string) {
	t.Helper()
	command := exec.Command("git", "add", "-A")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
}

func refreshFixture(t *testing.T, root string, previous *freshness.Manifest, force bool) freshness.Result {
	t.Helper()
	repo, err := repository.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	repo.Identity = "example.test/repository"
	result, err := (Provider{}).Refresh(context.Background(), freshness.Request{Repository: repo, Previous: previous, Force: force})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fixtureManifest(units []freshness.Unit) *freshness.Manifest {
	return &freshness.Manifest{Provider: (Provider{}).ID(), Units: append([]freshness.Unit(nil), units...)}
}

func combineFacts(batches []graph.UnitFacts) graph.UnitFacts {
	var result graph.UnitFacts
	for _, batch := range batches {
		result.Documents = append(result.Documents, batch.Documents...)
		result.Symbols = append(result.Symbols, batch.Symbols...)
		result.Occurrences = append(result.Occurrences, batch.Occurrences...)
		result.Edges = append(result.Edges, batch.Edges...)
	}
	return result
}

func symbolName(symbols []graph.Symbol, id string) string {
	for _, symbol := range symbols {
		if symbol.ID == id {
			return symbol.StableName
		}
	}
	return ""
}
