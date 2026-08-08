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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
