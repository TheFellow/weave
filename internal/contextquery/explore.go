package contextquery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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
	contextOptions.FullDefinitions = true
	contextOptions.OmitRelationshipSources = true
	contextOptions.LexicalTerms = exploreTerms(value)
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
	if shouldResolveExact(value) {
		if symbol, err := query.ResolveUnique(ctx, store, value); err == nil {
			return []graph.Symbol{symbol}, false, nil
		} else if !errors.Is(err, query.ErrAmbiguous) && !errors.Is(err, query.ErrNotFound) {
			return nil, false, err
		}
	}

	terms := exploreTerms(value)
	searchTerms := make([]string, 0, len(terms))
	for _, term := range terms {
		if !exploreScopeTerm(term) || term == "gui" || term == "tui" {
			searchTerms = append(searchTerms, term)
		}
	}
	if len(searchTerms) == 0 {
		searchTerms = terms
	}
	fetchLimit := min(512, max(limit*16, 64))
	byID := map[string]scoredSymbol{}
	truncated := false
	for _, term := range searchTerms {
		termMatches := map[string]scoredSymbol{}
		for _, variant := range exploreTermVariants(term) {
			matches, termTruncated, err := store.FindSymbols(ctx, variant, fetchLimit)
			if err != nil {
				return nil, false, err
			}
			truncated = truncated || termTruncated
			for index, symbol := range matches {
				score := max(1, 5-index/8) + exploreRarityScore(len(matches), termTruncated) + exploreNameScore(symbol, variant, len(terms) <= 12 && term == searchTerms[0])
				if current, exists := termMatches[symbol.ID]; !exists || score > current.score {
					termMatches[symbol.ID] = scoredSymbol{symbol: symbol, score: score}
				}
			}
		}
		for id, match := range termMatches {
			candidate := byID[id]
			candidate.symbol = match.symbol
			candidate.score += match.score
			byID[id] = candidate
		}
	}

	candidates := make([]scoredSymbol, 0, len(byID))
	for _, candidate := range byID {
		candidate.score += exploreKindScore(candidate.symbol.Kind)
		coverage := exploreSearchCoverage(candidate.symbol, searchTerms)
		candidate.score += coverage * 10
		if coverage >= 3 && coverage == len(searchTerms) && len(candidate.symbol.SearchTerms) <= max(256, len(searchTerms)*32) {
			candidate.score += 100
		}
		candidate.score += exploreContentSpecificityScore(candidate.symbol)
		if candidate.symbol.Evidence == graph.EvidenceGenerated {
			candidate.score -= 80
		}
		if strings.HasPrefix(candidate.symbol.DisplayName, "Test") {
			candidate.score -= 40
		}
		stable := strings.ToLower(candidate.symbol.StableName)
		for _, term := range terms {
			if strings.Contains(stable, term) {
				// Repository paths and stable semantic names are durable scope
				// signals even when that term was not the posting which first
				// discovered the candidate.
				candidate.score += 8
			}
			if exploreScopeTerm(term) && strings.Contains(stable, term) {
				candidate.score += exploreScopeScore(term)
			}
		}
		candidates = append(candidates, candidate)
	}
	candidates = applyContentDocumentScope(candidates)
	candidates = applyExplicitDomainScope(candidates, terms)
	slices.SortFunc(candidates, func(left, right scoredSymbol) int {
		if left.score != right.score {
			return right.score - left.score
		}
		if left.symbol.StableName != right.symbol.StableName {
			return strings.Compare(left.symbol.StableName, right.symbol.StableName)
		}
		return strings.Compare(left.symbol.ID, right.symbol.ID)
	})
	candidates = diversifyMethodContainers(candidates)
	candidates = diversifyContentNames(candidates)
	candidates = diversifyExplicitScopes(candidates, terms, limit)
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

func applyContentDocumentScope(candidates []scoredSymbol) []scoredSymbol {
	bestPath, bestScore := "", 0
	for _, candidate := range candidates {
		if candidate.symbol.Kind != "section" && candidate.symbol.Kind != "document" {
			continue
		}
		name := candidate.symbol.StableName
		if index := strings.IndexByte(name, '#'); index >= 0 {
			name = name[:index]
		}
		if bestPath == "" || candidate.score > bestScore || (candidate.score == bestScore && name < bestPath) {
			bestPath, bestScore = name, candidate.score
		}
	}
	if bestPath == "" {
		return candidates
	}
	count := 0
	for _, candidate := range candidates {
		if candidate.symbol.StableName == bestPath || strings.HasPrefix(candidate.symbol.StableName, bestPath+"#") {
			count++
		}
	}
	if count < 3 {
		return candidates
	}
	for index := range candidates {
		if candidates[index].symbol.StableName == bestPath || strings.HasPrefix(candidates[index].symbol.StableName, bestPath+"#") {
			candidates[index].score += 40
		}
	}
	return candidates
}

func exploreRarityScore(matches int, truncated bool) int {
	if truncated {
		return 1
	}
	switch {
	case matches <= 4:
		return 30
	case matches <= 16:
		return 18
	case matches <= 64:
		return 10
	case matches <= 256:
		return 4
	default:
		return 1
	}
}

func exploreContentSpecificityScore(symbol graph.Symbol) int {
	switch symbol.Kind {
	case "section", "document", "code-block":
		// A very broad generated page or document prelude can contain every
		// query token by accident. Prefer the smallest authored section that
		// explains the concept while leaving broad entities discoverable.
		return -min(600, max(0, len(symbol.SearchTerms)-64)/4)
	case "file":
		return -min(300, max(0, len(symbol.SearchTerms)-256)/8)
	default:
		return 0
	}
}

func exploreSearchCoverage(symbol graph.Symbol, terms []string) int {
	coverage := 0
	for _, term := range terms {
		for _, variant := range exploreTermVariants(term) {
			if _, found := slices.BinarySearch(symbol.SearchTerms, variant); found {
				coverage++
				break
			}
		}
	}
	return coverage
}

func shouldResolveExact(value string) bool {
	return len(value) <= 512 && len(strings.Fields(value)) <= 12
}

func diversifyContentNames(candidates []scoredSymbol) []scoredSymbol {
	preferred := make([]scoredSymbol, 0, len(candidates))
	overflow := make([]scoredSymbol, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.symbol.Kind != "section" && candidate.symbol.Kind != "document" {
			preferred = append(preferred, candidate)
			continue
		}
		name := strings.ToLower(candidate.symbol.DisplayName)
		if seen[name] {
			overflow = append(overflow, candidate)
			continue
		}
		seen[name] = true
		preferred = append(preferred, candidate)
	}
	return append(preferred, overflow...)
}

func diversifyMethodContainers(candidates []scoredSymbol) []scoredSymbol {
	preferred := make([]scoredSymbol, 0, len(candidates))
	overflow := make([]scoredSymbol, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		container := methodContainer(candidate.symbol.StableName)
		if container != "" && seen[container] {
			overflow = append(overflow, candidate)
			continue
		}
		if container != "" {
			seen[container] = true
		}
		preferred = append(preferred, candidate)
	}
	return append(preferred, overflow...)
}

func methodContainer(stableName string) string {
	if index := strings.Index(stableName, ".method."); index >= 0 {
		return stableName[:index]
	}
	return ""
}

// diversifyExplicitScopes preserves the best lexical ordering while ensuring
// an explicitly named presentation surface is represented. Natural research
// questions commonly ask for both GUI and TUI flow; one surface can otherwise
// consume the entire bound through several exact same-named methods.
func diversifyExplicitScopes(candidates []scoredSymbol, terms []string, limit int) []scoredSymbol {
	if len(candidates) <= limit || limit < 2 {
		return candidates
	}
	result := slices.Clone(candidates)
	primary := primaryExploreTerm(terms)
	for _, scope := range []string{"gui", "tui"} {
		if !slices.Contains(terms, scope) || containsScope(result[:limit], scope) {
			continue
		}
		candidateIndex := -1
		for index := limit; index < len(result); index++ {
			if symbolHasScope(result[index].symbol, scope) && symbolMatchesTerm(result[index].symbol, primary) {
				candidateIndex = index
				break
			}
		}
		if candidateIndex < 0 {
			for index := limit; index < len(result); index++ {
				if symbolHasScope(result[index].symbol, scope) {
					candidateIndex = index
					break
				}
			}
		}
		if candidateIndex < 0 {
			continue
		}
		replace := limit - 1
		result[replace], result[candidateIndex] = result[candidateIndex], result[replace]
	}
	return result
}

func primaryExploreTerm(terms []string) string {
	for _, term := range terms {
		if !exploreScopeTerm(term) {
			return term
		}
	}
	return ""
}

func symbolMatchesTerm(symbol graph.Symbol, term string) bool {
	return term != "" && (strings.Contains(strings.ToLower(symbol.DisplayName), term) || strings.Contains(strings.ToLower(symbol.StableName), term))
}

func containsScope(candidates []scoredSymbol, scope string) bool {
	return slices.ContainsFunc(candidates, func(candidate scoredSymbol) bool {
		return symbolHasScope(candidate.symbol, scope)
	})
}

func symbolHasScope(symbol graph.Symbol, scope string) bool {
	stable := strings.ToLower(symbol.StableName)
	return strings.Contains(stable, "/surfaces/"+scope+".") || strings.Contains(stable, "/surfaces/"+scope+"/")
}

func applyExplicitDomainScope(candidates []scoredSymbol, terms []string) []scoredSymbol {
	requestedDomains := map[string]bool{}
	for _, candidate := range candidates {
		domain := symbolDomain(candidate.symbol.StableName)
		for _, term := range terms {
			if domainMatchesTerm(domain, term) {
				requestedDomains[domain] = true
			}
		}
	}
	if len(requestedDomains) == 0 {
		return candidates
	}
	exactCounts := map[string]int{}
	for _, term := range terms {
		for _, variant := range exploreTermVariants(term) {
			for _, candidate := range candidates {
				if strings.EqualFold(candidate.symbol.DisplayName, variant) {
					exactCounts[variant]++
				}
			}
		}
	}
	result := make([]scoredSymbol, 0, len(candidates))
	for _, candidate := range candidates {
		if requestedDomains[symbolDomain(candidate.symbol.StableName)] || rareExactCandidate(candidate.symbol, exactCounts) {
			result = append(result, candidate)
		}
	}
	if len(result) < 2 {
		return candidates
	}
	return result
}

func symbolDomain(stableName string) string {
	const marker = "/domains/"
	index := strings.Index(stableName, marker)
	if index < 0 {
		return ""
	}
	remainder := stableName[index+len(marker):]
	if end := strings.IndexAny(remainder, "/."); end >= 0 {
		return remainder[:end]
	}
	return remainder
}

func domainMatchesTerm(domain, term string) bool {
	if domain == "" {
		return false
	}
	return domain == term || strings.TrimSuffix(domain, "s") == strings.TrimSuffix(term, "s")
}

func rareExactCandidate(symbol graph.Symbol, exactCounts map[string]int) bool {
	name := strings.ToLower(symbol.DisplayName)
	count := exactCounts[name]
	return count > 0 && count <= 2
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
	case "backend", "code", "data", "domain", "flow", "function", "gui", "menu", "method", "persistence", "public", "request", "state", "system", "tui":
		return true
	default:
		return false
	}
}

func exploreTerms(value string) []string {
	stop := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"alone": true, "be": true, "by": true, "can": true, "cannot": true, "describe": true,
		"direct": true, "does": true, "evaluate": true, "explain": true, "feed": true,
		"from": true, "how": true, "identify": true, "in": true, "initial": true,
		"into": true, "is": true, "it": true, "limitations": true, "of": true,
		"on": true, "or": true, "should": true, "that": true, "the": true, "this": true,
		"through": true, "to": true, "tool": true, "using": true, "what": true,
		"when": true, "where": true, "which": true, "why": true, "with": true,
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
		if len(result) == 32 {
			break
		}
	}
	if len(result) == 0 {
		return []string{strings.TrimSpace(value)}
	}
	return result
}

func exploreNameScore(symbol graph.Symbol, term string, primary bool) int {
	name := strings.ToLower(symbol.DisplayName)
	score := 0
	if name == term {
		if primary {
			score += 200
			if first, _ := utf8.DecodeRuneInString(name); unicode.IsLower(first) {
				score -= 120
			}
		} else {
			score += 35
		}
	} else if symbol.Kind != "package" && symbol.Kind != "file" && strings.Contains(name, term) {
		if primary {
			score += 60
		} else {
			score += 10
		}
	}
	return score
}

func exploreTermVariants(term string) []string {
	result := []string{term}
	if strings.HasSuffix(term, "ed") && len(term) > 4 {
		stem := strings.TrimSuffix(term, "ed")
		result = append(result, strings.TrimSuffix(term, "d"), stem, stem+"s")
	} else if strings.HasSuffix(term, "ing") && len(term) > 5 {
		stem := strings.TrimSuffix(term, "ing")
		result = append(result, stem, stem+"e")
	} else if strings.HasSuffix(term, "ies") && len(term) > 4 {
		result = append(result, strings.TrimSuffix(term, "ies")+"y", strings.TrimSuffix(term, "s"))
	} else if strings.HasSuffix(term, "es") && len(term) > 4 {
		result = append(result, strings.TrimSuffix(term, "es"), strings.TrimSuffix(term, "s"))
	} else if strings.HasSuffix(term, "s") && len(term) > 4 {
		result = append(result, strings.TrimSuffix(term, "s"))
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func exploreKindScore(kind string) int {
	switch kind {
	case "function", "method":
		return 100
	case "type", "interface", "test":
		return 40
	case "section":
		return 40
	case "code-block":
		return 20
	case "field", "constant", "route":
		return -10
	case "variable", "parameter":
		return -20
	case "asset", "directory", "url":
		return -30
	case "package", "file", "document":
		return -10
	default:
		return 0
	}
}
