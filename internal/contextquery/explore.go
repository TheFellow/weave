package contextquery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/query"
)

// ExploreOptions bound both candidate discovery and each source-rich dossier.
// MaxSourceBytes in Context is shared across all returned dossiers.
type ExploreOptions struct {
	FocusLimit int
	Context    Options
}

// Explore turns a natural research phrase or ambiguous symbol name into a
// small deterministic set of ordinary context results. It is lexical graph
// retrieval, not a second semantic index or an embedded language model.
func Explore(ctx context.Context, store Store, value string, options ExploreOptions, locate Locator) ([]Result, bool, error) {
	if options.FocusLimit < 1 || options.FocusLimit > 32 {
		return nil, false, errors.New("explore focus limit must be between 1 and 32")
	}
	if strings.TrimSpace(value) == "" {
		return nil, false, query.ErrNotFound
	}

	symbols, truncated, err := exploreCandidates(ctx, store, value, options.FocusLimit)
	if err != nil {
		return nil, false, err
	}
	if len(symbols) == 0 {
		return nil, truncated, query.ErrNotFound
	}

	contextOptions := options.Context
	contextOptions.MaxSourceBytes = max(1, options.Context.MaxSourceBytes/len(symbols))
	results := make([]Result, 0, len(symbols))
	for _, symbol := range symbols {
		result, buildErr := Build(ctx, store, symbol.ID, contextOptions, locate)
		if buildErr != nil {
			return nil, truncated, buildErr
		}
		result.Target = symbol.StableName
		results = append(results, result)
	}
	return results, truncated, nil
}

type scoredSymbol struct {
	symbol graph.Symbol
	score  int
}

func exploreCandidates(ctx context.Context, store Store, value string, limit int) ([]graph.Symbol, bool, error) {
	if symbol, err := query.ResolveUnique(ctx, store, value); err == nil {
		return []graph.Symbol{symbol}, false, nil
	} else if !errors.Is(err, query.ErrAmbiguous) && !errors.Is(err, query.ErrNotFound) {
		return nil, false, err
	}

	terms := exploreTerms(value)
	searchTerms := make([]string, 0, len(terms))
	for _, term := range terms {
		if !exploreScopeTerm(term) {
			searchTerms = append(searchTerms, term)
		}
	}
	if len(searchTerms) == 0 {
		searchTerms = terms
	}
	fetchLimit := min(512, max(limit*8, 32))
	byID := map[string]scoredSymbol{}
	truncated := false
	for _, term := range searchTerms {
		for _, variant := range exploreTermVariants(term) {
			matches, termTruncated, err := store.FindSymbols(ctx, variant, fetchLimit)
			if err != nil {
				return nil, false, err
			}
			truncated = truncated || termTruncated
			for index, symbol := range matches {
				candidate := byID[symbol.ID]
				candidate.symbol = symbol
				candidate.score += max(1, 5-index/8)
				candidate.score += exploreNameScore(symbol, variant)
				byID[symbol.ID] = candidate
			}
		}
	}

	candidates := make([]scoredSymbol, 0, len(byID))
	for _, candidate := range byID {
		candidate.score += exploreKindScore(candidate.symbol.Kind)
		stable := strings.ToLower(candidate.symbol.StableName)
		for _, term := range terms {
			if exploreScopeTerm(term) && strings.Contains(stable, term) {
				candidate.score += exploreScopeScore(term)
			}
		}
		candidates = append(candidates, candidate)
	}
	slices.SortFunc(candidates, func(left, right scoredSymbol) int {
		if left.score != right.score {
			return right.score - left.score
		}
		if left.symbol.StableName != right.symbol.StableName {
			return strings.Compare(left.symbol.StableName, right.symbol.StableName)
		}
		return strings.Compare(left.symbol.ID, right.symbol.ID)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
		truncated = true
	}
	result := make([]graph.Symbol, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.symbol)
	}
	return result, truncated, nil
}

func exploreScopeScore(term string) int {
	switch term {
	case "menu":
		return 30
	case "gui", "tui":
		return 8
	case "domain", "persistence":
		return 4
	default:
		return 2
	}
}

func exploreScopeTerm(term string) bool {
	switch term {
	case "code", "data", "domain", "flow", "function", "gui", "menu", "method", "persistence", "request", "state", "system", "tui":
		return true
	default:
		return false
	}
}

func exploreTerms(value string) []string {
	stop := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"be": true, "by": true, "does": true, "from": true, "how": true, "in": true,
		"into": true, "is": true, "it": true, "of": true, "on": true, "or": true,
		"that": true, "the": true, "this": true, "through": true, "to": true,
		"what": true, "when": true, "where": true, "which": true, "with": true,
	}
	seen := map[string]bool{}
	var result []string
	for _, term := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len(term) < 3 || stop[term] || seen[term] {
			continue
		}
		seen[term] = true
		result = append(result, term)
		if len(result) == 12 {
			break
		}
	}
	if len(result) == 0 {
		return []string{strings.TrimSpace(value)}
	}
	return result
}

func exploreNameScore(symbol graph.Symbol, term string) int {
	name := strings.ToLower(symbol.DisplayName)
	stable := strings.ToLower(symbol.StableName)
	score := 0
	if name == term {
		score += 100
	} else if symbol.Kind != "package" && symbol.Kind != "file" && strings.Contains(name, term) {
		score += 15
	}
	if strings.Contains(stable, term) {
		score += 2
	}
	return score
}

func exploreTermVariants(term string) []string {
	result := []string{term}
	if strings.HasSuffix(term, "ed") && len(term) > 4 {
		stem := strings.TrimSuffix(term, "d")
		if stem != term {
			result = append(result, stem)
		}
	} else if strings.HasSuffix(term, "ing") && len(term) > 5 {
		stem := strings.TrimSuffix(term, "ing")
		result = append(result, stem, stem+"e")
	} else if strings.HasSuffix(term, "s") && len(term) > 4 {
		result = append(result, strings.TrimSuffix(term, "s"))
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func exploreKindScore(kind string) int {
	switch kind {
	case "function", "method":
		return 50
	case "type", "interface", "test":
		return 20
	case "field", "constant", "route":
		return 5
	case "variable", "parameter":
		return -20
	case "package", "file", "document":
		return -10
	default:
		return 0
	}
}
