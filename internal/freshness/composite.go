package freshness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// CompositeProvider composes disjoint semantic producers into one atomic refresh.
// Inventory ownership prevents one producer from deleting another producer's facts.
type CompositeProvider struct {
	Providers []Provider
}

func (provider CompositeProvider) ID() ProviderID {
	ids := make([]ProviderID, 0, len(provider.Providers))
	for _, child := range provider.Providers {
		ids = append(ids, child.ID())
	}
	encoded, _ := json.Marshal(ids)
	digest := sha256.Sum256(encoded)
	return ProviderID{Name: "weave-composite", Version: "1." + hex.EncodeToString(digest[:6])}
}

func (provider CompositeProvider) Refresh(ctx context.Context, request Request) (Result, error) {
	result := Result{}
	active := make(map[string]bool, len(provider.Providers))
	seenUnits := map[string]string{}
	for _, child := range provider.Providers {
		owner := child.ID().Name
		if owner == "" || active[owner] {
			return Result{}, fmt.Errorf("duplicate or empty freshness provider owner %q", owner)
		}
		active[owner] = true
		childRequest := request
		childRequest.Previous = ownedManifest(request.Previous, child.ID(), owner)
		childResult, err := child.Refresh(ctx, childRequest)
		if err != nil {
			return Result{}, err
		}
		for i := range childResult.Units {
			childResult.Units[i].Owner = owner
			if previous, exists := seenUnits[childResult.Units[i].ID]; exists {
				return Result{}, fmt.Errorf("unit %q is owned by both %s and %s", childResult.Units[i].ID, previous, owner)
			}
			seenUnits[childResult.Units[i].ID] = owner
		}
		result.Batches = append(result.Batches, childResult.Batches...)
		result.Removed = append(result.Removed, childResult.Removed...)
		result.Units = append(result.Units, childResult.Units...)
	}
	if request.Previous != nil {
		for _, unit := range request.Previous.Units {
			if unit.Owner != "" && !active[unit.Owner] {
				result.Removed = append(result.Removed, unit.ID)
			}
		}
	}
	slices.SortFunc(result.Units, func(a, b Unit) int {
		if a.Owner != b.Owner {
			return strings.Compare(a.Owner, b.Owner)
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.Sort(result.Removed)
	result.Removed = slices.Compact(result.Removed)
	return result, nil
}

func ownedManifest(previous *Manifest, child ProviderID, owner string) *Manifest {
	if previous == nil {
		return nil
	}
	copy := *previous
	copy.Provider = child
	copy.Units = nil
	legacy := previous.Provider == child
	for _, unit := range previous.Units {
		if unit.Owner == owner || (legacy && unit.Owner == "") {
			copy.Units = append(copy.Units, unit)
		}
	}
	return &copy
}
