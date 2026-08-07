package goindex

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"golang.org/x/tools/go/packages"
)

type packageAnalysis struct {
	repository string
	root       string
	pkg        *packages.Package
	facts      graph.UnitFacts
	objects    map[types.Object]string
	types      map[*types.TypeName]string
	interfaces map[*types.TypeName]*types.Interface
	concretes  map[*types.TypeName]types.Type
	functions  []functionExtent
	localPaths map[string]bool
	version    string
}

type functionExtent struct {
	start token.Pos
	end   token.Pos
	id    string
}

func analyzePackage(repository, root string, pkg *packages.Package, surfaces map[string]string, localPaths map[string]bool, version string) (*packageAnalysis, error) {
	unitID := semanticID("unit", repository, pkg.PkgPath)
	input, err := inputFingerprint(root, pkg, surfaces)
	if err != nil {
		return nil, err
	}
	analysis := &packageAnalysis{
		repository: repository, root: root, pkg: pkg,
		objects: map[types.Object]string{}, types: map[*types.TypeName]string{}, localPaths: localPaths, version: version,
		interfaces: map[*types.TypeName]*types.Interface{}, concretes: map[*types.TypeName]types.Type{},
		facts: graph.UnitFacts{Unit: graph.Unit{
			ID: unitID, Provider: "weave-go", ProviderVersion: version,
			Language: "go", Variant: variantName(pkg), InputFingerprint: input,
			SurfaceFingerprint: surfaces[pkg.PkgPath],
		}},
	}
	analysis.discoverObjectOwners()
	if err := analysis.addDocuments(); err != nil {
		return nil, err
	}
	analysis.addPackageSymbol()
	analysis.addDefinitions()
	analysis.addOccurrencesAndEdges()
	return analysis, nil
}

func variantName(pkg *packages.Package) string {
	if pkg.ForTest == "" {
		return "default"
	}
	if pkg.PkgPath == pkg.ForTest {
		return "test"
	}
	return "external-test:" + pkg.ForTest
}

func (analysis *packageAnalysis) discoverObjectOwners() {
	scope := analysis.pkg.Types.Scope()
	for _, name := range scope.Names() {
		object := scope.Lookup(name)
		if object == nil {
			continue
		}
		analysis.objects[object] = "package/" + name
		typeName, ok := object.(*types.TypeName)
		if !ok {
			continue
		}
		named := namedType(typeName.Type())
		if named == nil {
			continue
		}
		analysis.types[typeName] = analysis.objectID(typeName)
		if iface, ok := named.Underlying().(*types.Interface); ok {
			iface.Complete()
			analysis.interfaces[typeName] = iface
		} else {
			analysis.concretes[typeName] = named
		}
		for method := range named.Methods() {
			analysis.objects[method] = "type/" + name + "/method/" + method.Name()
		}
		analysis.ownMembers(name, named.Underlying())
	}
}

func (analysis *packageAnalysis) ownMembers(owner string, typ types.Type) {
	switch value := typ.(type) {
	case *types.Struct:
		for field := range value.Fields() {
			analysis.objects[field] = "type/" + owner + "/field/" + field.Name()
		}
	case *types.Interface:
		value.Complete()
		for method := range value.ExplicitMethods() {
			analysis.objects[method] = "type/" + owner + "/method/" + method.Name()
		}
	}
}

func namedType(typ types.Type) *types.Named {
	switch value := typ.(type) {
	case *types.Named:
		return value.Origin()
	case *types.Alias:
		return namedType(types.Unalias(value))
	default:
		return nil
	}
}

func (analysis *packageAnalysis) addDocuments() error {
	for _, name := range analysis.pkg.CompiledGoFiles {
		path, ok := relativePath(analysis.root, name)
		if !ok {
			continue
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		analysis.facts.Documents = append(analysis.facts.Documents, graph.Document{
			ID: analysis.documentID(path), UnitID: analysis.facts.Unit.ID, Path: path, Language: "go",
			ContentHash: "sha256:" + hex.EncodeToString(digest[:]), Provider: "weave-go", ProviderVersion: analysis.version,
		})
	}
	return nil
}

func (analysis *packageAnalysis) addPackageSymbol() {
	analysis.facts.Symbols = append(analysis.facts.Symbols, graph.Symbol{
		ID: analysis.packageID(analysis.pkg.PkgPath), UnitID: analysis.facts.Unit.ID,
		StableName: analysis.pkg.PkgPath, DisplayName: analysis.pkg.PkgPath, Kind: "package",
		NormalizedName: graph.NormalizeName(analysis.pkg.PkgPath),
		Provider:       "weave-go", Evidence: graph.EvidenceExact,
	})
}

func (analysis *packageAnalysis) addDefinitions() {
	for identifier, object := range analysis.pkg.TypesInfo.Defs {
		if object == nil || identifier.Name == "_" {
			continue
		}
		id := analysis.objectID(object)
		analysis.objects[object] = analysis.objectPath(object)
		documentID, sourceRange, ok := analysis.source(identifier.Pos(), identifier.End())
		if !ok {
			continue
		}
		symbol := graph.Symbol{
			ID: id, UnitID: analysis.facts.Unit.ID, StableName: analysis.stableName(object),
			DisplayName: object.Name(), NormalizedName: graph.NormalizeName(object.Name()), Kind: objectKind(object), DocumentID: documentID,
			Definition: sourceRange, Provider: "weave-go", Evidence: graph.EvidenceExact,
		}
		analysis.facts.Symbols = append(analysis.facts.Symbols, symbol)
		analysis.facts.Occurrences = append(analysis.facts.Occurrences, analysis.occurrence("definition", id, documentID, sourceRange))
		analysis.facts.Edges = append(analysis.facts.Edges, analysis.edge(
			analysis.packageID(analysis.pkg.PkgPath), id, graph.EdgeDefines, documentID, sourceRange,
		))
		if owner := analysis.memberOwner(object); owner != "" {
			analysis.facts.Edges = append(analysis.facts.Edges, analysis.edge(owner, id, graph.EdgeContains, documentID, sourceRange))
		}
		if _, ok := object.(*types.Func); ok {
			analysis.functions = append(analysis.functions, functionExtent{start: identifier.Pos(), end: enclosingFunctionEnd(analysis.pkg.Syntax, identifier.Pos()), id: id})
		}
	}
	// Defs positions point at function names, so correct extents from AST bodies.
	for _, file := range analysis.pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.FuncDecl)
			if !ok || declaration.Name == nil {
				return true
			}
			object, _ := analysis.pkg.TypesInfo.Defs[declaration.Name].(*types.Func)
			if object == nil {
				return true
			}
			id := analysis.objectID(object)
			analysis.functions = append(analysis.functions, functionExtent{start: declaration.Pos(), end: declaration.End(), id: id})
			return false
		})
	}
}

func enclosingFunctionEnd(files []*ast.File, position token.Pos) token.Pos {
	for _, file := range files {
		if file.Pos() <= position && position <= file.End() {
			return file.End()
		}
	}
	return position
}

func (analysis *packageAnalysis) addOccurrencesAndEdges() {
	for identifier, object := range analysis.pkg.TypesInfo.Uses {
		if object == nil || identifier.Name == "_" {
			continue
		}
		documentID, sourceRange, ok := analysis.source(identifier.Pos(), identifier.End())
		if !ok {
			continue
		}
		target := analysis.objectID(object)
		analysis.facts.Occurrences = append(analysis.facts.Occurrences, analysis.occurrence("reference", target, documentID, sourceRange))
		analysis.facts.Edges = append(analysis.facts.Edges, analysis.edge(
			analysis.ownerAt(identifier.Pos()), target, graph.EdgeReferences, documentID, sourceRange,
		))
	}
	for _, file := range analysis.pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.ImportSpec:
				imported := analysis.pkg.TypesInfo.Implicits[value]
				if value.Name != nil {
					imported = analysis.pkg.TypesInfo.Defs[value.Name]
				}
				packageName, _ := imported.(*types.PkgName)
				if packageName == nil || packageName.Imported() == nil {
					return true
				}
				documentID, sourceRange, ok := analysis.source(value.Pos(), value.End())
				if !ok {
					return true
				}
				target := analysis.packageID(packageName.Imported().Path())
				analysis.facts.Edges = append(analysis.facts.Edges,
					analysis.edge(analysis.packageID(analysis.pkg.PkgPath), target, graph.EdgeImports, documentID, sourceRange),
					analysis.edge(analysis.packageID(analysis.pkg.PkgPath), target, graph.EdgeDependsOn, documentID, sourceRange),
				)
			case *ast.CallExpr:
				object := analysis.calledObject(value.Fun)
				function, ok := object.(*types.Func)
				if !ok {
					return true
				}
				documentID, sourceRange, ok := analysis.source(value.Fun.Pos(), value.Fun.End())
				if ok {
					analysis.facts.Edges = append(analysis.facts.Edges, analysis.edge(
						analysis.ownerAt(value.Pos()), analysis.objectID(function), graph.EdgeCalls, documentID, sourceRange,
					))
				}
			}
			return true
		})
	}
}

func (analysis *packageAnalysis) calledObject(expression ast.Expr) types.Object {
	switch value := expression.(type) {
	case *ast.Ident:
		return analysis.pkg.TypesInfo.ObjectOf(value)
	case *ast.SelectorExpr:
		if selection := analysis.pkg.TypesInfo.Selections[value]; selection != nil {
			return selection.Obj()
		}
		return analysis.pkg.TypesInfo.ObjectOf(value.Sel)
	case *ast.IndexExpr:
		return analysis.calledObject(value.X)
	case *ast.IndexListExpr:
		return analysis.calledObject(value.X)
	case *ast.ParenExpr:
		return analysis.calledObject(value.X)
	default:
		return nil
	}
}

func addImplementations(analyses []*packageAnalysis) {
	type candidate struct {
		analysis *packageAnalysis
		name     types.Object
		iface    *types.Interface
		id       string
	}
	byRequiredMethod := map[string][]candidate{}
	for _, interfacePackage := range analyses {
		for interfaceName, iface := range interfacePackage.interfaces {
			if iface.NumMethods() == 0 {
				continue
			}
			value := candidate{
				analysis: interfacePackage, name: interfaceName, iface: iface,
				id: interfacePackage.objectID(interfaceName),
			}
			byRequiredMethod[methodKey(iface.Method(0))] = append(byRequiredMethod[methodKey(iface.Method(0))], value)
		}
	}
	for key := range byRequiredMethod {
		slices.SortFunc(byRequiredMethod[key], func(a, b candidate) int { return strings.Compare(a.id, b.id) })
	}

	for _, concretePackage := range analyses {
		for concreteName, typ := range concretePackage.concretes {
			named := namedType(typ)
			if named == nil || named.TypeParams().Len() != 0 {
				// Implements is intentionally unspecified for an uninstantiated
				// generic named type. Do not label an approximation Exact.
				continue
			}
			pointerMethods := types.NewMethodSet(types.NewPointer(typ))
			possible := map[string]candidate{}
			for method := range pointerMethods.Methods() {
				for _, value := range byRequiredMethod[methodKey(method.Obj())] {
					possible[value.id] = value
				}
			}
			candidates := make([]candidate, 0, len(possible))
			for _, value := range possible {
				candidates = append(candidates, value)
			}
			slices.SortFunc(candidates, func(a, b candidate) int { return strings.Compare(a.id, b.id) })
			for _, value := range candidates {
				implementationType := typ
				if !types.Implements(implementationType, value.iface) {
					implementationType = types.NewPointer(typ)
				}
				if !types.Implements(implementationType, value.iface) {
					continue
				}
				from := concretePackage.objectID(concreteName)
				concretePackage.facts.Edges = append(concretePackage.facts.Edges, concretePackage.edge(from, value.id, graph.EdgeImplements, "", graph.Range{}))
				methods := types.NewMethodSet(implementationType)
				for abstract := range value.iface.Methods() {
					selection := methods.Lookup(abstract.Pkg(), abstract.Name())
					if selection == nil {
						continue
					}
					concretePackage.facts.Edges = append(concretePackage.facts.Edges, concretePackage.edge(
						concretePackage.objectID(selection.Obj()), value.analysis.objectID(abstract),
						graph.EdgeImplements, "", graph.Range{},
					))
				}
			}
		}
	}
}

func methodKey(method types.Object) string {
	if method.Exported() {
		return method.Name()
	}
	if method.Pkg() == nil {
		return "\x00" + method.Name()
	}
	return method.Pkg().Path() + "\x00" + method.Name()
}

func (analysis *packageAnalysis) finish() {
	dedupeSymbols(&analysis.facts.Symbols)
	dedupeOccurrences(&analysis.facts.Occurrences)
	dedupeEdges(&analysis.facts.Edges)
	slices.SortFunc(analysis.facts.Documents, func(a, b graph.Document) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(analysis.facts.Symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(analysis.facts.Occurrences, func(a, b graph.Occurrence) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(analysis.facts.Edges, func(a, b graph.Edge) int { return strings.Compare(a.ID, b.ID) })
	copy := analysis.facts
	copy.Unit.InventoryDigest = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(encoded)
	analysis.facts.Unit.InventoryDigest = "sha256:" + hex.EncodeToString(digest[:])
}

func dedupeSymbols(values *[]graph.Symbol) {
	seen := map[string]bool{}
	result := (*values)[:0]
	for _, value := range *values {
		if !seen[value.ID] {
			seen[value.ID] = true
			result = append(result, value)
		}
	}
	*values = result
}

func dedupeOccurrences(values *[]graph.Occurrence) {
	seen := map[string]bool{}
	result := (*values)[:0]
	for _, value := range *values {
		if !seen[value.ID] {
			seen[value.ID] = true
			result = append(result, value)
		}
	}
	*values = result
}

func dedupeEdges(values *[]graph.Edge) {
	seen := map[string]bool{}
	result := (*values)[:0]
	for _, value := range *values {
		if !seen[value.ID] {
			seen[value.ID] = true
			result = append(result, value)
		}
	}
	*values = result
}

func dedupeGlobalEdges(analyses []*packageAnalysis) {
	seen := map[string]bool{}
	for _, analysis := range analyses {
		values := analysis.facts.Edges[:0]
		for _, edge := range analysis.facts.Edges {
			if seen[edge.ID] {
				continue
			}
			seen[edge.ID] = true
			values = append(values, edge)
		}
		analysis.facts.Edges = values
	}
}

func (analysis *packageAnalysis) ownerAt(position token.Pos) string {
	owner := analysis.packageID(analysis.pkg.PkgPath)
	width := token.Pos(^uint(0) >> 1)
	for _, function := range analysis.functions {
		if function.start <= position && position <= function.end && function.end-function.start < width {
			owner = function.id
			width = function.end - function.start
		}
	}
	return owner
}

func (analysis *packageAnalysis) memberOwner(object types.Object) string {
	path := analysis.objectPath(object)
	parts := strings.Split(path, "/")
	if len(parts) >= 4 && parts[0] == "type" {
		if owner := analysis.pkg.Types.Scope().Lookup(parts[1]); owner != nil {
			return analysis.objectID(owner)
		}
	}
	return ""
}

func (analysis *packageAnalysis) objectPath(object types.Object) string {
	if path := analysis.objects[object]; path != "" {
		return path
	}
	if function, ok := object.(*types.Func); ok {
		if signature, _ := function.Type().(*types.Signature); signature != nil && signature.Recv() != nil {
			if receiver := receiverName(signature.Recv().Type()); receiver != "" {
				return "type/" + receiver + "/method/" + object.Name()
			}
		}
	}
	if object.Pkg() != nil && object.Parent() == object.Pkg().Scope() {
		return "package/" + object.Name()
	}
	if owner := memberOwnerInPackage(object); owner != "" {
		return "type/" + owner + "/" + objectKind(object) + "/" + object.Name()
	}
	position := analysis.pkg.Fset.PositionFor(object.Pos(), false)
	path, ok := relativePath(analysis.root, position.Filename)
	if !ok {
		path = filepath.ToSlash(position.Filename)
	}
	return objectKind(object) + "/" + object.Name() + "@" + path + ":" + strconv.Itoa(position.Offset)
}

func receiverName(typ types.Type) string {
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = pointer.Elem()
	}
	if named := namedType(typ); named != nil && named.Obj() != nil {
		return named.Obj().Name()
	}
	return ""
}

func memberOwnerInPackage(object types.Object) string {
	if object.Pkg() == nil {
		return ""
	}
	scope := object.Pkg().Scope()
	for _, name := range scope.Names() {
		typeName, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		named := namedType(typeName.Type())
		if named == nil {
			continue
		}
		for method := range named.Methods() {
			if method == object {
				return name
			}
		}
		switch underlying := named.Underlying().(type) {
		case *types.Struct:
			for field := range underlying.Fields() {
				if field == object {
					return name
				}
			}
		case *types.Interface:
			underlying.Complete()
			for method := range underlying.ExplicitMethods() {
				if method == object {
					return name
				}
			}
		}
	}
	return ""
}

func (analysis *packageAnalysis) objectID(object types.Object) string {
	if object == nil {
		return ""
	}
	pkg := object.Pkg()
	if pkg == nil {
		return semanticID("go-external", "builtin", analysis.objectPath(object))
	}
	repository := analysis.repository
	if pkg.Path() != analysis.pkg.PkgPath && !analysis.localPackagePath(pkg.Path()) {
		repository = "external"
	}
	return semanticID("go", repository, pkg.Path(), analysis.objectPath(object))
}

func (analysis *packageAnalysis) localPackagePath(path string) bool {
	return analysis.localPaths[path]
}

func (analysis *packageAnalysis) packageID(path string) string {
	repository := analysis.repository
	if path != analysis.pkg.PkgPath && !analysis.localPackagePath(path) {
		repository = "external"
	}
	return semanticID("go-package", repository, path)
}

func (analysis *packageAnalysis) stableName(object types.Object) string {
	if object.Pkg() == nil {
		return object.Name()
	}
	return object.Pkg().Path() + "." + strings.ReplaceAll(analysis.objectPath(object), "/", ".")
}

func (analysis *packageAnalysis) documentID(path string) string {
	return semanticID("document", analysis.repository, path)
}

func semanticID(kind string, parts ...string) string {
	encoded := make([]string, len(parts))
	for i, part := range parts {
		encoded[i] = base64.RawURLEncoding.EncodeToString([]byte(part))
	}
	return kind + ":" + strings.Join(encoded, ":")
}

func objectKind(object types.Object) string {
	switch value := object.(type) {
	case *types.Builtin:
		return "builtin"
	case *types.Const:
		return "constant"
	case *types.Func:
		if signature, _ := value.Type().(*types.Signature); signature != nil && signature.Recv() != nil {
			return "method"
		}
		return "function"
	case *types.Label:
		return "label"
	case *types.PkgName:
		return "import"
	case *types.TypeName:
		return "type"
	case *types.Var:
		if value.IsField() {
			return "field"
		}
		return "variable"
	default:
		return "symbol"
	}
}

func (analysis *packageAnalysis) source(start, end token.Pos) (string, graph.Range, bool) {
	from := analysis.pkg.Fset.PositionFor(start, false)
	to := analysis.pkg.Fset.PositionFor(end, false)
	path, ok := relativePath(analysis.root, from.Filename)
	if !ok || from.Filename != to.Filename || from.Line < 1 || from.Column < 1 {
		return "", graph.Range{}, false
	}
	return analysis.documentID(path), graph.Range{
		Start: graph.Position{Line: int32(from.Line - 1), Column: int32(from.Column - 1), Byte: int64(from.Offset)},
		End:   graph.Position{Line: int32(to.Line - 1), Column: int32(to.Column - 1), Byte: int64(to.Offset)},
	}, true
}

func (analysis *packageAnalysis) occurrence(role, symbol, document string, sourceRange graph.Range) graph.Occurrence {
	id := semanticID("occurrence", role, symbol, document, rangeKey(sourceRange))
	return graph.Occurrence{ID: id, UnitID: analysis.facts.Unit.ID, SymbolID: symbol, DocumentID: document, Role: role, Range: sourceRange, Provider: "weave-go", Evidence: graph.EvidenceExact}
}

func (analysis *packageAnalysis) edge(from, to string, kind graph.EdgeKind, document string, sourceRange graph.Range) graph.Edge {
	id := semanticID("edge", string(kind), from, to, document, rangeKey(sourceRange))
	return graph.Edge{ID: id, UnitID: analysis.facts.Unit.ID, From: from, To: to, Kind: kind, Evidence: graph.EvidenceExact, DocumentID: document, Range: sourceRange, Provider: "weave-go"}
}

func rangeKey(value graph.Range) string {
	return fmt.Sprintf("%d:%d", value.Start.Byte, value.End.Byte)
}
