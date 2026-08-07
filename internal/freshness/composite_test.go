package freshness

import (
	"context"
	"reflect"
	"testing"
)

func TestCompositeProviderKeepsInventoriesDisjointAndRemovesAbsentOwner(t *testing.T) {
	left := compositeFixture{id: ProviderID{Name: "left", Version: "1"}, result: Result{Units: []Unit{{ID: "l"}}}}
	right := compositeFixture{id: ProviderID{Name: "right", Version: "1"}, result: Result{Units: []Unit{{ID: "r"}}}}
	provider := CompositeProvider{Providers: []Provider{left, right}}
	result, err := provider.Refresh(context.Background(), Request{Previous: &Manifest{Units: []Unit{{ID: "old-left", Owner: "left"}, {ID: "gone", Owner: "gone"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Units, []Unit{{ID: "l", Owner: "left"}, {ID: "r", Owner: "right"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %#v, want %#v", got, want)
	}
	if got, want := result.Removed, []string{"gone", "old-left"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed = %q, want %q", got, want)
	}
}

type compositeFixture struct {
	id     ProviderID
	result Result
}

func (fixture compositeFixture) ID() ProviderID { return fixture.id }
func (fixture compositeFixture) Refresh(_ context.Context, request Request) (Result, error) {
	result := fixture.result
	if request.Previous != nil {
		for _, unit := range request.Previous.Units {
			if unit.ID != result.Units[0].ID {
				result.Removed = append(result.Removed, unit.ID)
			}
		}
	}
	return result, nil
}
