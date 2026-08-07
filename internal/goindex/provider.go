// Package goindex implements Weave's compiler-native Go semantic provider.
package goindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/types"
	goversion "go/version"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/freshness"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

const providerVersion = "1"

// Provider indexes the active Go build without permitting dependency download
// or go.mod/go.sum mutation. It preserves the user's Go toolchain selection so
// an auto-selected, already installed toolchain is not replaced by an older
// base executable. Empty non-Go repositories produce an empty index.
type Provider struct {
	// AllowNetwork permits the Go command to use the configured module proxy.
	// The default is false; dependencies already present in the module cache work.
	AllowNetwork bool
	// BuildFlags are passed to go list in addition to -mod=readonly.
	BuildFlags []string
}

func (provider Provider) ID() freshness.ProviderID {
	configuration := []string{runtime.Version(), runtime.GOOS, runtime.GOARCH}
	for _, name := range []string{"GOOS", "GOARCH", "GOFLAGS", "GOWORK", "CGO_ENABLED"} {
		configuration = append(configuration, name+"="+os.Getenv(name))
	}
	configuration = append(configuration, provider.BuildFlags...)
	digest := digestStrings(configuration)
	return freshness.ProviderID{Name: "weave-go", Version: providerVersion + "." + digest[len("sha256:"):len("sha256:")+12]}
}

// Refresh loads one consistent compiler universe and returns complete package
// inventory, replacing only units whose semantic fingerprints changed.
func (provider Provider) Refresh(ctx context.Context, request freshness.Request) (freshness.Result, error) {
	root := request.Repository.Root
	if !hasGoWorkspace(root) {
		return emptyResult(request.Previous), nil
	}
	if err := checkTargetGoVersion(root); err != nil {
		return freshness.Result{}, err
	}
	configuration := &packages.Config{
		Context: ctx,
		Dir:     root,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule |
			packages.NeedForTest,
		Tests:      true,
		Env:        goEnvironment(provider.AllowNetwork),
		BuildFlags: append([]string{"-mod=readonly"}, provider.BuildFlags...),
	}
	loaded, err := packages.Load(configuration, "./...")
	if err != nil {
		return freshness.Result{}, fmt.Errorf("load Go packages: %w", err)
	}
	if err := packageErrors(loaded); err != nil {
		return freshness.Result{}, err
	}
	selected, err := selectPackages(root, loaded)
	if err != nil {
		return freshness.Result{}, err
	}
	if len(selected) == 0 {
		return emptyResult(request.Previous), nil
	}

	previous := previousUnits(request.Previous)
	surfaces := allSurfaces(loaded)
	localPackages := make(map[string]bool, len(selected))
	for _, pkg := range selected {
		localPackages[pkg.PkgPath] = true
	}
	analyses := make([]*packageAnalysis, 0, len(selected))
	for _, pkg := range selected {
		analysis, err := analyzePackage(request.Repository.Identity, root, pkg, surfaces, localPackages, provider.ID().Version)
		if err != nil {
			return freshness.Result{}, fmt.Errorf("index Go package %s: %w", pkg.PkgPath, err)
		}
		analyses = append(analyses, analysis)
	}

	// Interface satisfaction needs the single shared types universe and may
	// connect units, so add it after each package's local symbols are known.
	addImplementations(analyses)
	// One exact implementation relationship can be discovered while analyzing
	// several packages that reuse the same concrete/interface pair. Facts have
	// globally stable IDs, so assign each duplicate to the first package in the
	// canonical package order rather than making persistence order-dependent.
	dedupeGlobalEdges(analyses)

	result := freshness.Result{}
	result.Units = make([]freshness.Unit, 0, len(analyses))
	for _, analysis := range analyses {
		analysis.finish()
		unit := freshness.Unit{
			ID: analysis.facts.Unit.ID, InputFingerprint: analysis.facts.Unit.InputFingerprint,
			SurfaceFingerprint: analysis.facts.Unit.SurfaceFingerprint,
			InventoryDigest:    analysis.facts.Unit.InventoryDigest,
		}
		result.Units = append(result.Units, unit)
		if old, ok := previous[unit.ID]; request.Force || !ok || old != unit {
			result.Batches = append(result.Batches, analysis.facts)
		}
		delete(previous, unit.ID)
	}
	for id := range previous {
		result.Removed = append(result.Removed, id)
	}
	slices.SortFunc(result.Units, func(a, b freshness.Unit) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(result.Removed)
	return result, nil
}

func checkTargetGoVersion(root string) error {
	type versionFile struct {
		name  string
		parse func([]byte) (string, error)
	}
	files := []versionFile{
		{"go.mod", func(content []byte) (string, error) {
			parsed, err := modfile.ParseLax("go.mod", content, nil)
			if err != nil || parsed.Go == nil {
				return "", err
			}
			return parsed.Go.Version, nil
		}},
		{"go.work", func(content []byte) (string, error) {
			parsed, err := modfile.ParseWork("go.work", content, nil)
			if err != nil || parsed.Go == nil {
				return "", err
			}
			return parsed.Go.Version, nil
		}},
	}
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(root, file.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s Go version: %w", file.name, err)
		}
		required, err := file.parse(content)
		if err != nil {
			return fmt.Errorf("parse %s Go version: %w", file.name, err)
		}
		if required == "" {
			continue
		}
		target := "go" + required
		if goversion.IsValid(target) && goversion.IsValid(runtime.Version()) && goversion.Compare(target, runtime.Version()) > 0 {
			return fmt.Errorf("target %s requires Go %s, but this Weave binary was built with %s; install or build Weave with Go %s or newer (runtime indexing does not download toolchains)", file.name, required, runtime.Version(), required)
		}
	}
	return nil
}

func hasGoWorkspace(root string) bool {
	for _, name := range []string{"go.work", "go.mod"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

func goEnvironment(allowNetwork bool) []string {
	environment := append([]string(nil), os.Environ()...)
	if !allowNetwork {
		// GOPROXY=off prevents both module and missing toolchain downloads. Keep
		// GOSUMDB intact because Go requires checksum verification even for an
		// auto-selected toolchain already present in the local module cache.
		environment = withoutEnvironment(environment, "GOPROXY", "GONOSUMDB")
		environment = append(environment, "GOPROXY=off", "GONOSUMDB=*")
	}
	return environment
}

func withoutEnvironment(environment []string, names ...string) []string {
	result := environment[:0]
	for _, value := range environment {
		name, _, _ := strings.Cut(value, "=")
		if slices.Contains(names, name) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func emptyResult(previous *freshness.Manifest) freshness.Result {
	result := freshness.Result{Units: []freshness.Unit{}}
	if previous != nil {
		for _, unit := range previous.Units {
			result.Removed = append(result.Removed, unit.ID)
		}
		slices.Sort(result.Removed)
	}
	return result
}

func previousUnits(manifest *freshness.Manifest) map[string]freshness.Unit {
	units := map[string]freshness.Unit{}
	if manifest != nil {
		for _, unit := range manifest.Units {
			units[unit.ID] = unit
		}
	}
	return units
}

func packageErrors(roots []*packages.Package) error {
	var messages []string
	packages.Visit(roots, func(pkg *packages.Package) bool {
		for _, problem := range pkg.Errors {
			messages = append(messages, problem.Error())
			if len(messages) == 20 {
				return false
			}
		}
		return len(messages) < 20
	}, nil)
	if len(messages) == 0 {
		return nil
	}
	slices.Sort(messages)
	return fmt.Errorf("Go package load failed: %s", strings.Join(messages, "; "))
}

func selectPackages(root string, roots []*packages.Package) ([]*packages.Package, error) {
	groups := map[string][]*packages.Package{}
	packages.Visit(roots, func(pkg *packages.Package) bool {
		if packageInside(root, pkg) && !isSyntheticTestMain(pkg) {
			groups[pkg.PkgPath] = append(groups[pkg.PkgPath], pkg)
		}
		return true
	}, nil)
	selected := make([]*packages.Package, 0, len(groups))
	for _, candidates := range groups {
		slices.SortFunc(candidates, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
		choice := candidates[0]
		for _, candidate := range candidates {
			// Prefer the in-package test variant so ordinary source and _test.go
			// definitions are represented exactly once.
			if candidate.ForTest == candidate.PkgPath && strings.Contains(candidate.ID, "[") {
				choice = candidate
				break
			}
			if candidate.ForTest == "" {
				choice = candidate
			}
		}
		selected = append(selected, choice)
	}
	slices.SortFunc(selected, func(a, b *packages.Package) int { return strings.Compare(a.PkgPath, b.PkgPath) })
	return selected, nil
}

func packageInside(root string, pkg *packages.Package) bool {
	for _, name := range pkg.CompiledGoFiles {
		if _, ok := relativePath(root, name); ok {
			return true
		}
	}
	return false
}

func isSyntheticTestMain(pkg *packages.Package) bool {
	return pkg.Name == "main" && pkg.ForTest != "" && strings.HasSuffix(pkg.PkgPath, ".test")
}

func allSurfaces(roots []*packages.Package) map[string]string {
	result := map[string]string{}
	chosen := map[string]string{}
	packages.Visit(roots, func(pkg *packages.Package) bool {
		if pkg.Types != nil && pkg.PkgPath != "" {
			// Prefer the production package surface. Test augmentation must not
			// invalidate dependents when only a _test.go declaration changes.
			if _, ok := result[pkg.PkgPath]; !ok || (pkg.ForTest == "" && chosen[pkg.PkgPath] != "production") {
				result[pkg.PkgPath] = surfaceFingerprint(pkg.Types)
				if pkg.ForTest == "" {
					chosen[pkg.PkgPath] = "production"
				}
			}
		}
		return true
	}, nil)
	return result
}

func surfaceFingerprint(pkg *types.Package) string {
	if pkg == nil {
		return digestStrings(nil)
	}
	qualifier := func(other *types.Package) string {
		if other == nil {
			return ""
		}
		return other.Path()
	}
	scope := pkg.Scope()
	var records []string
	for _, name := range scope.Names() {
		object := scope.Lookup(name)
		if object == nil || !object.Exported() {
			continue
		}
		record := types.ObjectString(object, qualifier)
		if constant, ok := object.(*types.Const); ok {
			record += "=" + constant.Val().ExactString()
		}
		records = append(records, record)
		if typeName, ok := object.(*types.TypeName); ok {
			if named := namedType(typeName.Type()); named != nil {
				for method := range named.Methods() {
					records = append(records, "method "+types.ObjectString(method, qualifier))
				}
			}
		}
	}
	slices.Sort(records)
	return digestStrings(records)
}

func relativePath(root, name string) (string, bool) {
	relative, err := filepath.Rel(root, name)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func digestStrings(values []string) string {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func inputFingerprint(root string, pkg *packages.Package, surfaces map[string]string) (string, error) {
	records := []string{
		"package=" + pkg.PkgPath, "variant=" + pkg.ID, "for-test=" + pkg.ForTest,
		"toolchain=" + runtime.Version(), "goos=" + runtime.GOOS, "goarch=" + runtime.GOARCH,
	}
	for _, name := range pkg.CompiledGoFiles {
		path, ok := relativePath(root, name)
		if !ok {
			continue
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		digest := sha256.Sum256(content)
		records = append(records, "file="+path+":"+hex.EncodeToString(digest[:]))
	}
	for _, name := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err == nil {
			digest := sha256.Sum256(content)
			records = append(records, "manifest="+name+":"+hex.EncodeToString(digest[:]))
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
	}
	if pkg.Module != nil && pkg.Module.GoMod != "" {
		for _, name := range []string{pkg.Module.GoMod, filepath.Join(filepath.Dir(pkg.Module.GoMod), "go.sum")} {
			content, err := os.ReadFile(name)
			if err == nil {
				digest := sha256.Sum256(content)
				path, ok := relativePath(root, name)
				if !ok {
					path = filepath.Base(name)
				}
				records = append(records, "module-manifest="+path+":"+hex.EncodeToString(digest[:]))
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("read module manifest: %w", err)
			}
		}
	}
	imports := make([]string, 0, len(pkg.Imports))
	for path := range pkg.Imports {
		imports = append(imports, "dependency="+path+":"+surfaces[path])
	}
	slices.Sort(imports)
	records = append(records, imports...)
	slices.Sort(records)
	return digestStrings(records), nil
}
