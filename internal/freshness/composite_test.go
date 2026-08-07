package freshness

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/storage"
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

func TestCompositeManagerPublishesProviderFactsWithoutCrossDeletion(t *testing.T) {
	root := freshRepository(t)
	left := compositeFixture{id: ProviderID{Name: "left", Version: "1"}, result: Result{Batches: []graph.UnitFacts{{Unit: graph.Unit{ID: "left-unit", Provider: "left", ProviderVersion: "1"}}}, Units: []Unit{{ID: "left-unit"}}}}
	right := compositeFixture{id: ProviderID{Name: "right", Version: "1"}, result: Result{Batches: []graph.UnitFacts{{Unit: graph.Unit{ID: "right-unit", Provider: "right", ProviderVersion: "1"}}}, Units: []Unit{{ID: "right-unit"}}}}
	manager := Manager{Directory: root, Provider: CompositeProvider{Providers: []Provider{left, right}}, Command: "test"}
	if _, err := manager.Ensure(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	left.result.Batches[0].Unit.InventoryDigest = "changed"
	right.result.Batches = nil
	manager.Provider = CompositeProvider{Providers: []Provider{left, right}}
	if _, err := manager.Ensure(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	path, err := manager.DatabasePath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(context.Background(), path, storage.Options{MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := db.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Units) != 2 || snapshot.Units[0].ID != "left-unit" || snapshot.Units[1].ID != "right-unit" {
		t.Fatalf("units after refresh = %#v", snapshot.Units)
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
