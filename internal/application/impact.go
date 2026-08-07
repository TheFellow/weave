package application

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
)

func impactRoots(snapshot graph.Snapshot, files, packages []string) ([]string, []string, error) {
	documentsByPath := make(map[string][]string, len(snapshot.Documents))
	for _, document := range snapshot.Documents {
		path := filepath.ToSlash(filepath.Clean(document.Path))
		documentsByPath[path] = append(documentsByPath[path], document.ID)
	}
	pathSymbols := map[string][]string{}
	for _, symbol := range snapshot.Symbols {
		if symbol.Kind == "file" || symbol.Kind == "asset" || symbol.Kind == "symlink" || symbol.Kind == "document" {
			pathSymbols[filepath.ToSlash(filepath.Clean(symbol.StableName))] = append(pathSymbols[filepath.ToSlash(filepath.Clean(symbol.StableName))], symbol.ID)
		}
	}
	rootSet := map[string]bool{}
	missing := []string{}
	fileSet := map[string]bool{}
	for _, value := range files {
		path, ok := safeGraphPath(value)
		if !ok {
			return nil, nil, fmt.Errorf("impact file must be a repository-relative path: %q", value)
		}
		fileSet[path] = true
		for _, id := range pathSymbols[path] {
			rootSet[id] = true
		}
		if len(documentsByPath[path]) == 0 && len(pathSymbols[path]) == 0 {
			missing = append(missing, "file:"+path)
		}
	}
	packageUnits := map[string]bool{}
	for _, value := range packages {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil, fmt.Errorf("impact package is empty")
		}
		matched := false
		for _, symbol := range snapshot.Symbols {
			if symbol.Kind == "package" && (symbol.StableName == value || symbol.DisplayName == value) {
				rootSet[symbol.ID], packageUnits[symbol.UnitID], matched = true, true, true
			}
		}
		if !matched {
			missing = append(missing, "package:"+value)
		}
	}
	selectedDocuments := map[string]bool{}
	for path, ids := range documentsByPath {
		if fileSet[path] {
			for _, id := range ids {
				selectedDocuments[id] = true
			}
		}
	}
	for _, symbol := range snapshot.Symbols {
		if selectedDocuments[symbol.DocumentID] || packageUnits[symbol.UnitID] {
			rootSet[symbol.ID] = true
		}
	}
	for _, occurrence := range snapshot.Occurrences {
		if selectedDocuments[occurrence.DocumentID] {
			rootSet[occurrence.SymbolID] = true
		}
	}
	for _, edge := range snapshot.Edges {
		if selectedDocuments[edge.DocumentID] {
			rootSet[edge.From], rootSet[edge.To] = true, true
		}
	}
	roots := make([]string, 0, len(rootSet))
	for id := range rootSet {
		if id != "" {
			roots = append(roots, id)
		}
	}
	slices.Sort(roots)
	slices.Sort(missing)
	missing = slices.Compact(missing)
	var diagnostics []string
	if len(missing) != 0 {
		diagnostics = append(diagnostics, "impact roots absent from current graph: "+strings.Join(missing, ", "))
	}
	if len(roots) == 0 {
		return nil, diagnostics, fmt.Errorf("no indexed graph roots matched the requested files or packages: %s", strings.Join(missing, ", "))
	}
	return roots, diagnostics, nil
}

func affectedTests(snapshot graph.Snapshot, nodes []string) []graph.Symbol {
	impacted := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		impacted[node] = true
	}
	documents := make(map[string]graph.Document, len(snapshot.Documents))
	for _, document := range snapshot.Documents {
		documents[document.ID] = document
	}
	explicit := map[string]bool{}
	for _, edge := range snapshot.Edges {
		if edge.Kind == graph.EdgeTests && impacted[edge.From] {
			explicit[edge.From] = true
		}
	}
	var result []graph.Symbol
	for _, symbol := range snapshot.Symbols {
		if !impacted[symbol.ID] {
			continue
		}
		path := filepath.ToSlash(documents[symbol.DocumentID].Path)
		goTest := strings.HasSuffix(path, "_test.go") && (strings.HasPrefix(symbol.DisplayName, "Test") || strings.HasPrefix(symbol.DisplayName, "Benchmark") || strings.HasPrefix(symbol.DisplayName, "Fuzz"))
		if explicit[symbol.ID] || goTest || symbol.Kind == "test" {
			result = append(result, symbol)
		}
	}
	slices.SortFunc(result, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func safeGraphPath(value string) (string, bool) {
	if value == "" || filepath.IsAbs(value) {
		return "", false
	}
	path := filepath.ToSlash(filepath.Clean(value))
	return path, path != "." && path != ".." && !strings.HasPrefix(path, "../")
}
