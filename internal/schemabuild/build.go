package schemabuild

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"
)

type buildModel struct {
	file         sourceFile
	ecosystem    string
	name         string
	dependencies []buildDependency
	targets      []buildTarget
	generated    []buildGeneration
}

type buildDependency struct {
	name, localManifest string
	rng                 graph.Range
}

type buildTarget struct {
	name      string
	evidence  graph.Evidence
	rng       graph.Range
	dependsOn []string
}

type buildGeneration struct {
	source, target string
	rng            graph.Range
}

func parseBuild(builder *factBuilder, _ string, files []sourceFile) ([]string, error) {
	models := make([]buildModel, 0, len(files))
	var warnings []string
	for _, file := range files {
		model, err := parseBuildManifest(file)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("build manifest %s omitted: %v", file.path, err))
			continue
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return warnings, fmt.Errorf("no valid build manifests")
	}
	byManifest := map[string]string{}
	for _, model := range models {
		byManifest[model.file.path] = builder.localID("build/project", buildProjectKey(model))
	}
	for _, model := range models {
		projectRange := findTokenRange(model.file.data, model.name, 0)
		if projectRange.Start.Byte < 0 {
			_, displayToken, _ := strings.Cut(model.name, ":")
			if displayToken != "" {
				projectRange = findTokenRange(model.file.data, displayToken, 0)
			}
		}
		projectID := builder.addSymbol("build/project", buildProjectKey(model), model.name, "build-project-"+model.ecosystem, model.file.path, projectRange, graph.EvidenceDeclared)
		localTargets := map[string]bool{}
		for _, target := range model.targets {
			localTargets[target.name] = true
		}
		for _, target := range model.targets {
			targetID := builder.addSymbol("build/target", buildProjectKey(model)+":"+target.name, target.name, "build-target", model.file.path, target.rng, target.evidence)
			builder.addEdge(projectID, targetID, graph.EdgeContains, model.file.path, target.rng, target.evidence)
			for _, dependency := range target.dependsOn {
				dependencyID := openID("build/target/"+model.ecosystem, dependency)
				if localTargets[dependency] {
					dependencyID = builder.localID("build/target", buildProjectKey(model)+":"+dependency)
				}
				builder.addReference(dependencyID, model.file.path, target.rng, graph.EvidenceDeclared)
				builder.addEdge(targetID, dependencyID, graph.EdgeDependsOn, model.file.path, target.rng, graph.EvidenceDeclared)
			}
		}
		for _, dependency := range model.dependencies {
			target := openID("build/dependency/"+model.ecosystem, dependency.name)
			if dependency.localManifest != "" {
				if local, ok := byManifest[dependency.localManifest]; ok {
					target = local
				}
			}
			builder.addReference(target, model.file.path, dependency.rng, graph.EvidenceDeclared)
			builder.addEdge(projectID, target, graph.EdgeDependsOn, model.file.path, dependency.rng, graph.EvidenceDeclared)
		}
		for _, generated := range model.generated {
			builder.addEdge(graph.WorkspacePathID(builder.repository, generated.source), graph.WorkspacePathID(builder.repository, generated.target), graph.EdgeGenerates, model.file.path, generated.rng, graph.EvidenceGenerated)
		}
	}
	slices.Sort(warnings)
	return warnings, nil
}

func buildProjectKey(model buildModel) string {
	return model.ecosystem + ":" + model.name + "@" + model.file.path
}

func parseBuildManifest(file sourceFile) (buildModel, error) {
	base := strings.ToLower(path.Base(file.path))
	switch {
	case base == "go.mod":
		return parseGoMod(file)
	case base == "cargo.toml":
		return parseCargo(file)
	case base == "package.json":
		return parsePackageJSON(file)
	case base == "pom.xml":
		return parseMaven(file)
	case path.Ext(base) == ".csproj" || path.Ext(base) == ".fsproj" || path.Ext(base) == ".vbproj":
		return parseMSBuild(file)
	default:
		return buildModel{}, fmt.Errorf("unsupported build manifest")
	}
}

func defaultBuildModel(file sourceFile, ecosystem, name string) buildModel {
	return buildModel{file: file, ecosystem: ecosystem, name: name}
}

func parseGoMod(file sourceFile) (buildModel, error) {
	parsed, err := modfile.Parse(file.path, file.data, nil)
	if err != nil {
		return buildModel{}, err
	}
	if parsed.Module == nil || parsed.Module.Mod.Path == "" {
		return buildModel{}, fmt.Errorf("missing module declaration")
	}
	model := defaultBuildModel(file, "go", parsed.Module.Mod.Path)
	replacements := map[string]string{}
	for _, replacement := range parsed.Replace {
		if replacement.New.Version == "" && replacement.New.Path != "" {
			replacements[replacement.Old.Path] = localManifest(file.path, replacement.New.Path, "go.mod")
		}
	}
	for _, requirement := range parsed.Require {
		model.dependencies = append(model.dependencies, buildDependency{name: requirement.Mod.Path, localManifest: replacements[requirement.Mod.Path], rng: findTokenRange(file.data, requirement.Mod.Path, 0)})
	}
	return model, nil
}

type cargoManifest struct {
	Package struct {
		Name string `toml:"name"`
	} `toml:"package"`
	Workspace struct {
		Members []string `toml:"members"`
	} `toml:"workspace"`
	Dependencies      map[string]any `toml:"dependencies"`
	DevDependencies   map[string]any `toml:"dev-dependencies"`
	BuildDependencies map[string]any `toml:"build-dependencies"`
	Lib               struct {
		Name string `toml:"name"`
	} `toml:"lib"`
	Bin []struct {
		Name string `toml:"name"`
	} `toml:"bin"`
}

func parseCargo(file sourceFile) (buildModel, error) {
	var parsed cargoManifest
	if err := toml.Unmarshal(file.data, &parsed); err != nil {
		return buildModel{}, err
	}
	name := parsed.Package.Name
	if name == "" {
		name = "workspace@" + path.Dir(file.path)
	}
	model := defaultBuildModel(file, "cargo", name)
	for _, dependencies := range []map[string]any{parsed.Dependencies, parsed.DevDependencies, parsed.BuildDependencies} {
		for dependency, raw := range dependencies {
			item := buildDependency{name: dependency, rng: findTokenRange(file.data, dependency, 0)}
			if table, ok := raw.(map[string]any); ok {
				if value, ok := table["path"].(string); ok {
					item.localManifest = localManifest(file.path, value, "Cargo.toml")
				}
			}
			model.dependencies = append(model.dependencies, item)
		}
	}
	for _, member := range parsed.Workspace.Members {
		model.dependencies = append(model.dependencies, buildDependency{name: "workspace-member:" + member, localManifest: localManifest(file.path, member, "Cargo.toml"), rng: findTokenRange(file.data, member, 0)})
	}
	if parsed.Lib.Name != "" {
		model.targets = append(model.targets, buildTarget{name: "lib:" + parsed.Lib.Name, evidence: graph.EvidenceDeclared, rng: findTokenRange(file.data, parsed.Lib.Name, 0)})
	}
	for _, binary := range parsed.Bin {
		if binary.Name != "" {
			model.targets = append(model.targets, buildTarget{name: "bin:" + binary.Name, evidence: graph.EvidenceDeclared, rng: findTokenRange(file.data, binary.Name, 0)})
		}
	}
	normalizeBuildModel(&model)
	return model, nil
}

type packageManifest struct {
	Name                 string            `json:"name"`
	Scripts              map[string]string `json:"scripts"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Workspaces           json.RawMessage   `json:"workspaces"`
}

func parsePackageJSON(file sourceFile) (buildModel, error) {
	var parsed packageManifest
	if err := json.Unmarshal(file.data, &parsed); err != nil {
		return buildModel{}, err
	}
	if parsed.Name == "" {
		return buildModel{}, fmt.Errorf("missing package name")
	}
	model := defaultBuildModel(file, "npm", parsed.Name)
	for name := range parsed.Scripts {
		model.targets = append(model.targets, buildTarget{name: "script:" + name, evidence: graph.EvidenceDeclared, rng: findTokenRange(file.data, name, 0)})
	}
	for _, dependencies := range []map[string]string{parsed.Dependencies, parsed.DevDependencies, parsed.PeerDependencies, parsed.OptionalDependencies} {
		for name, specification := range dependencies {
			item := buildDependency{name: name, rng: findTokenRange(file.data, name, 0)}
			if strings.HasPrefix(specification, "file:") {
				item.localManifest = localManifest(file.path, strings.TrimPrefix(specification, "file:"), "package.json")
			}
			model.dependencies = append(model.dependencies, item)
		}
	}
	workspaces, err := packageWorkspaces(parsed.Workspaces)
	if err != nil {
		return buildModel{}, err
	}
	for _, workspace := range workspaces {
		item := buildDependency{name: "workspace:" + workspace, rng: findTokenRange(file.data, workspace, 0)}
		if !strings.ContainsAny(workspace, "*?[") {
			item.localManifest = localManifest(file.path, workspace, "package.json")
		}
		model.dependencies = append(model.dependencies, item)
	}
	normalizeBuildModel(&model)
	return model, nil
}

func packageWorkspaces(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("workspaces must be an array or an object with packages: %w", err)
	}
	return object.Packages, nil
}

type mavenProject struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Parent     struct {
		GroupID      string  `xml:"groupId"`
		ArtifactID   string  `xml:"artifactId"`
		RelativePath *string `xml:"relativePath"`
	} `xml:"parent"`
	Dependencies []struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
	} `xml:"dependencies>dependency"`
	Modules []string `xml:"modules>module"`
}

func parseMaven(file sourceFile) (buildModel, error) {
	var parsed mavenProject
	if err := xml.Unmarshal(file.data, &parsed); err != nil {
		return buildModel{}, err
	}
	if parsed.ArtifactID == "" {
		return buildModel{}, fmt.Errorf("missing artifactId")
	}
	group := parsed.GroupID
	if group == "" {
		group = parsed.Parent.GroupID
	}
	name := strings.TrimPrefix(group+":"+parsed.ArtifactID, ":")
	model := defaultBuildModel(file, "maven", name)
	if parsed.Parent.ArtifactID != "" {
		relative := "../pom.xml"
		if parsed.Parent.RelativePath != nil {
			relative = *parsed.Parent.RelativePath
		}
		if relative != "" && path.Base(relative) != "pom.xml" {
			relative = path.Join(relative, "pom.xml")
		}
		local := ""
		if relative != "" {
			local = localManifestFile(file.path, relative)
		}
		model.dependencies = append(model.dependencies, buildDependency{name: strings.TrimPrefix(parsed.Parent.GroupID+":"+parsed.Parent.ArtifactID, ":"), localManifest: local, rng: findTokenRange(file.data, parsed.Parent.ArtifactID, 0)})
	}
	for _, dependency := range parsed.Dependencies {
		name := strings.TrimPrefix(dependency.GroupID+":"+dependency.ArtifactID, ":")
		model.dependencies = append(model.dependencies, buildDependency{name: name, rng: findTokenRange(file.data, dependency.ArtifactID, 0)})
	}
	for _, module := range parsed.Modules {
		model.dependencies = append(model.dependencies, buildDependency{name: "module:" + module, localManifest: localManifest(file.path, module, "pom.xml"), rng: findTokenRange(file.data, module, 0)})
	}
	normalizeBuildModel(&model)
	return model, nil
}

type msbuildProject struct {
	PropertyGroups []struct {
		AssemblyName  string `xml:"AssemblyName"`
		RootNamespace string `xml:"RootNamespace"`
	} `xml:"PropertyGroup"`
	ItemGroups []struct {
		ProjectReferences []struct {
			Include string `xml:"Include,attr"`
		} `xml:"ProjectReference"`
		PackageReferences []struct {
			Include string `xml:"Include,attr"`
		} `xml:"PackageReference"`
		Compile []struct {
			Include       string `xml:"Include,attr"`
			Update        string `xml:"Update,attr"`
			AutoGen       string `xml:"AutoGen"`
			DependentUpon string `xml:"DependentUpon"`
		} `xml:"Compile"`
	} `xml:"ItemGroup"`
	Targets []struct {
		Name             string `xml:"Name,attr"`
		DependsOnTargets string `xml:"DependsOnTargets,attr"`
	} `xml:"Target"`
}

func parseMSBuild(file sourceFile) (buildModel, error) {
	var parsed msbuildProject
	if err := xml.Unmarshal(file.data, &parsed); err != nil {
		return buildModel{}, err
	}
	name := strings.TrimSuffix(path.Base(file.path), path.Ext(file.path))
	for _, properties := range parsed.PropertyGroups {
		if properties.AssemblyName != "" {
			name = properties.AssemblyName
			break
		}
	}
	model := defaultBuildModel(file, "msbuild", name)
	for _, group := range parsed.ItemGroups {
		for _, reference := range group.ProjectReferences {
			model.dependencies = append(model.dependencies, buildDependency{name: reference.Include, localManifest: localManifestFile(file.path, filepathSlash(reference.Include)), rng: findTokenRange(file.data, reference.Include, 0)})
		}
		for _, reference := range group.PackageReferences {
			model.dependencies = append(model.dependencies, buildDependency{name: reference.Include, rng: findTokenRange(file.data, reference.Include, 0)})
		}
		for _, compile := range group.Compile {
			generated := compile.Include
			if generated == "" {
				generated = compile.Update
			}
			if strings.EqualFold(strings.TrimSpace(compile.AutoGen), "true") && generated != "" && compile.DependentUpon != "" {
				target := localSourcePath(file.path, generated)
				dependent := filepathSlash(compile.DependentUpon)
				if target == "" || !literalBuildPath(dependent) {
					continue
				}
				source := path.Clean(path.Join(path.Dir(target), dependent))
				if validPath(source) && !strings.HasPrefix(source, "../") {
					model.generated = append(model.generated, buildGeneration{source: source, target: target, rng: findTokenRange(file.data, generated, 0)})
				}
			}
		}
	}
	for _, target := range parsed.Targets {
		if target.Name == "" {
			continue
		}
		model.targets = append(model.targets, buildTarget{name: target.Name, evidence: graph.EvidenceDeclared, rng: findTokenRange(file.data, target.Name, 0), dependsOn: splitMSBuildList(target.DependsOnTargets)})
	}
	normalizeBuildModel(&model)
	return model, nil
}

func localManifest(manifest, relative, filename string) string {
	return localManifestFile(manifest, path.Join(filepathSlash(relative), filename))
}

func localManifestFile(manifest, relative string) string {
	relative = filepathSlash(relative)
	if !literalBuildPath(relative) {
		return ""
	}
	candidate := path.Clean(path.Join(path.Dir(manifest), relative))
	if !validPath(candidate) || strings.HasPrefix(candidate, "../") {
		return ""
	}
	return candidate
}

func literalBuildPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "$(") || strings.Contains(value, "${") {
		return false
	}
	return len(value) < 2 || value[1] != ':' || (value[0] < 'A' || value[0] > 'Z') && (value[0] < 'a' || value[0] > 'z')
}

func localSourcePath(manifest, relative string) string {
	return localManifestFile(manifest, filepathSlash(relative))
}

func filepathSlash(value string) string { return strings.ReplaceAll(value, "\\", "/") }

func splitMSBuildList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ";") {
		item = strings.TrimSpace(item)
		if item != "" && !strings.Contains(item, "$(") {
			result = append(result, item)
		}
	}
	return result
}

func normalizeBuildModel(model *buildModel) {
	slices.SortFunc(model.dependencies, func(a, b buildDependency) int {
		return strings.Compare(a.name+"\x00"+a.localManifest, b.name+"\x00"+b.localManifest)
	})
	model.dependencies = slices.CompactFunc(model.dependencies, func(a, b buildDependency) bool { return a.name == b.name && a.localManifest == b.localManifest })
	slices.SortFunc(model.targets, func(a, b buildTarget) int { return strings.Compare(a.name, b.name) })
	model.targets = slices.CompactFunc(model.targets, func(a, b buildTarget) bool { return a.name == b.name })
	slices.SortFunc(model.generated, func(a, b buildGeneration) int {
		return strings.Compare(a.source+"\x00"+a.target, b.source+"\x00"+b.target)
	})
}
