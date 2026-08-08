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
