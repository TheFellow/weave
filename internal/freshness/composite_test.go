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
	left := compositeFixture{id: ProviderID{Name: "left", Version: "1"}, result: Result{Units: []Unit{{ID: "l"}}, Diagnostics: []string{"degraded docs/a.md"}}}
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
	if got, want := result.Diagnostics, []string{"left: degraded docs/a.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}

func TestCompositeProviderPassesOwnerlessUnitsToChildren(t *testing.T) {
	child := &capturingCompositeFixture{id: ProviderID{Name: "child", Version: "1"}}
	provider := CompositeProvider{Providers: []Provider{child}}
	previous := &Manifest{Units: []Unit{{ID: "unit", Owner: "child", InputFingerprint: "same"}}}
	result, err := provider.Refresh(context.Background(), Request{Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	if child.previous.Owner != "" {
		t.Fatalf("child previous owner = %q, want empty", child.previous.Owner)
	}
	if len(result.Batches) != 0 || len(result.Units) != 1 || result.Units[0].Owner != "child" {
		t.Fatalf("unexpected reused result: %#v", result)
	}
}

func TestCompositeProviderUpgradesLegacyDirectManifest(t *testing.T) {
	child := &capturingCompositeFixture{id: ProviderID{Name: "child", Version: "1"}}
	provider := CompositeProvider{Providers: []Provider{child}}
	previous := &Manifest{Provider: child.id, Units: []Unit{{ID: "unit", InputFingerprint: "same"}, {ID: "stale"}}}
	result, err := provider.Refresh(context.Background(), Request{Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Removed, []string{"stale"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed = %q, want %q", got, want)
	}
	if err := validateResult(result, previous); err != nil {
		t.Fatalf("legacy upgrade did not validate: %v", err)
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

type capturingCompositeFixture struct {
	id       ProviderID
	previous Unit
}

func (fixture *capturingCompositeFixture) ID() ProviderID { return fixture.id }
func (fixture *capturingCompositeFixture) Refresh(_ context.Context, request Request) (Result, error) {
	if request.Previous != nil && len(request.Previous.Units) != 0 {
		fixture.previous = request.Previous.Units[0]
	}
	return Result{Units: []Unit{{ID: "unit", InputFingerprint: "same"}}}, nil
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
