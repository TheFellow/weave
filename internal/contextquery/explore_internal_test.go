package contextquery

import (
	"testing"

	"github.com/TheFellow/weave/internal/graph"
)

func TestExplicitDomainScopeDropsSameNamedForeignDomainAndKeepsRareCrossCuttingMatch(t *testing.T) {
	candidates := []scoredSymbol{
		{symbol: graph.Symbol{ID: "menus", StableName: "example/app/domains/menus/internal/commands.type.Commands.method.Publish", DisplayName: "Publish", Kind: "method"}},
		{symbol: graph.Symbol{ID: "drinks", StableName: "example/app/domains/drinks/surfaces/gui.type.Presenter.method.Publish", DisplayName: "Publish", Kind: "method"}},
		{symbol: graph.Symbol{ID: "audit", StableName: "example/app/domains/audit/surfaces/gui.type.Presenter.method.Publish", DisplayName: "Publish", Kind: "method"}},
		{symbol: graph.Symbol{ID: "authorize", StableName: "example/pkg/authz.package.Authorize", DisplayName: "Authorize", Kind: "function"}},
	}
	got := applyExplicitDomainScope(candidates, []string{"menu", "publish", "authorized"})
	if len(got) != 2 || got[0].symbol.ID != "menus" || got[1].symbol.ID != "authorize" {
		t.Fatalf("scoped candidates = %#v", got)
	}
}

func TestRelationshipProximityPrefersSameUnitThenSameRepositoryPath(t *testing.T) {
	focus := graph.Symbol{UnitID: "menus", StableName: "github.com/example/app/domains/menus/surfaces/gui.type.Presenter.method.Publish"}
	sameUnit := Relationship{Entity: &Entity{Symbol: graph.Symbol{UnitID: "menus", StableName: "github.com/example/app/domains/menus/surfaces/gui.type.Presenter.method.actionEnabled"}}}
	sameDomain := Relationship{Entity: &Entity{Symbol: graph.Symbol{UnitID: "commands", StableName: "github.com/example/app/domains/menus/internal/commands.type.Commands.method.Publish"}}}
	external := Relationship{Entity: &Entity{Symbol: graph.Symbol{UnitID: "time", StableName: "time.package.Now"}}}
	if relationshipProximity(focus, sameUnit) <= relationshipProximity(focus, sameDomain) {
		t.Fatalf("same unit was not preferred")
	}
	if relationshipProximity(focus, sameDomain) <= relationshipProximity(focus, external) {
		t.Fatalf("same domain was not preferred")
	}
}

func TestExploreTermsRetainEvidenceTermsFromLongResearchQuestion(t *testing.T) {
	terms := exploreTerms("Explain how a menu publish request is authorized from GUI TUI through the public domain module command middleware readiness validation persistence and tests")
	for _, want := range []string{"menu", "publish", "gui", "tui", "middleware", "readiness", "persistence", "tests"} {
		if !containsString(terms, want) {
			t.Fatalf("terms %v omit %q", terms, want)
		}
	}
}

func TestExploreTermsDoNotLoseTheEndOfAnAgentResearchQuestion(t *testing.T) {
	question := "Explain why query latency alone is insufficient to evaluate Weave as an agent research tool. Describe the limitations of the initial uncontrolled dogfood, how observed agent friction should feed product changes, how the paired with-Weave and without-Weave arms isolate the tool, which artifacts and measurements are retained, how answer correctness is checked, and how the first paired result should and should not be interpreted."
	terms := exploreTerms(question)
	for _, want := range []string{"latency", "uncontrolled", "friction", "artifacts", "measurements", "correctness", "result"} {
		if !containsString(terms, want) {
			t.Fatalf("terms %v omit %q", terms, want)
		}
	}
	if shouldResolveExact(question) {
		t.Fatal("long research question was treated as one exact symbol query")
	}
}

func TestDiversifyExplicitScopesRetainsGUIAndTUI(t *testing.T) {
	candidates := []scoredSymbol{
		{symbol: graph.Symbol{ID: "gui-publish", StableName: "example/app/domains/menus/surfaces/gui.type.Presenter.method.Publish"}, score: 100},
		{symbol: graph.Symbol{ID: "module", StableName: "example/app/domains/menus.type.Module.method.Publish"}, score: 90},
		{symbol: graph.Symbol{ID: "command", StableName: "example/app/domains/menus/internal/commands.type.Commands.method.Publish"}, score: 80},
		{symbol: graph.Symbol{ID: "tui-publish", StableName: "example/app/domains/menus/surfaces/tui.type.ListViewModel.method.performPublish"}, score: 70},
	}
	got := diversifyExplicitScopes(candidates, []string{"menu", "publish", "gui", "tui"}, 3)
	if !containsScope(got[:3], "gui") || !containsScope(got[:3], "tui") {
		t.Fatalf("diversified candidates = %#v", got[:3])
	}
}

func TestDiversifyMethodContainersDefersSiblingHelpers(t *testing.T) {
	candidates := []scoredSymbol{
		{symbol: graph.Symbol{ID: "publish", StableName: "example/surfaces/gui.type.Presenter.method.Publish"}},
		{symbol: graph.Symbol{ID: "helper", StableName: "example/surfaces/gui.type.Presenter.method.publish"}},
		{symbol: graph.Symbol{ID: "module", StableName: "example/domains/menus.type.Module.method.Publish"}},
	}
	got := diversifyMethodContainers(candidates)
	if got[0].symbol.ID != "publish" || got[1].symbol.ID != "module" || got[2].symbol.ID != "helper" {
		t.Fatalf("diversified candidates = %#v", got)
	}
}

func TestDiversifyContentNamesDefersGeneratedCopies(t *testing.T) {
	candidates := []scoredSymbol{
		{symbol: graph.Symbol{ID: "authored", Kind: "section", DisplayName: "Storage model"}},
		{symbol: graph.Symbol{ID: "generated", Kind: "section", DisplayName: "Storage model"}},
		{symbol: graph.Symbol{ID: "constraints", Kind: "section", DisplayName: "Concurrency constraints"}},
	}
	got := diversifyContentNames(candidates)
	if got[0].symbol.ID != "authored" || got[1].symbol.ID != "constraints" || got[2].symbol.ID != "generated" {
		t.Fatalf("diversified content candidates = %#v", got)
	}
}

func TestExploreContentSpecificityPenalizesBroadGeneratedRepresentations(t *testing.T) {
	specific := graph.Symbol{Kind: "section", SearchTerms: make([]string, 80), Evidence: graph.EvidenceSyntactic}
	broad := graph.Symbol{Kind: "section", SearchTerms: make([]string, 2048), Evidence: graph.EvidenceGenerated}
	if exploreContentSpecificityScore(specific) <= exploreContentSpecificityScore(broad) {
		t.Fatalf("specific score %d did not beat broad score %d", exploreContentSpecificityScore(specific), exploreContentSpecificityScore(broad))
	}
}

func TestExploreRarityPrefersDiscriminatingTerms(t *testing.T) {
	if exploreRarityScore(4, false) <= exploreRarityScore(64, false) || exploreRarityScore(64, false) <= exploreRarityScore(512, true) {
		t.Fatal("rarity score does not prefer bounded uncommon terms")
	}
}

func TestExploreTermVariantsHandleCommonSuffixes(t *testing.T) {
	for input, want := range map[string]string{"retained": "retains", "measurements": "measurement", "losses": "loss", "queries": "query"} {
		if !containsString(exploreTermVariants(input), want) {
			t.Fatalf("variants for %q = %v, omit %q", input, exploreTermVariants(input), want)
		}
	}
}

func TestContentDocumentScopeKeepsRelatedSectionsTogether(t *testing.T) {
	candidates := []scoredSymbol{
		{symbol: graph.Symbol{StableName: "guide.md#result", Kind: "section"}, score: 100},
		{symbol: graph.Symbol{StableName: "other.md#noise", Kind: "section"}, score: 95},
		{symbol: graph.Symbol{StableName: "guide.md#controls", Kind: "section"}, score: 70},
		{symbol: graph.Symbol{StableName: "guide.md#limits", Kind: "section"}, score: 60},
	}
	got := applyContentDocumentScope(candidates)
	if got[2].score != 110 || got[3].score != 100 || got[1].score != 95 {
		t.Fatalf("scoped scores = %#v", got)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
