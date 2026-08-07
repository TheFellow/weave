package adapter

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// BuiltinRoutingClaims reserves inputs already owned by precise in-process
// providers so broad external fallbacks cannot duplicate them.
func BuiltinRoutingClaims() []Registration {
	return []Registration{{Name: "weave-go", Claims: Claims{
		Inputs: Inputs{
			Extensions: []string{".go"},
			Filenames:  []string{"go.mod", "go.sum", "go.work", "go.work.sum"},
		},
		Evidence: []string{"exact"},
	}}}
}

// ValidateAutomaticClaims includes the in-process precise owners that are not
// represented in a user-managed registry.
func ValidateAutomaticClaims(registrations []Registration) error {
	for _, builtin := range BuiltinRoutingClaims() {
		for _, registration := range registrations {
			if registration.Claims.Fallback {
				continue
			}
			if claim, ok := overlappingClaim(builtin.Claims.Inputs, registration.Claims.Inputs); ok {
				return fmt.Errorf("adapter claim conflict: %q and %q both own %s", builtin.Name, registration.Name, claim)
			}
		}
	}
	return ValidateClaimOverlap(registrations)
}

// MergeRegistrations gives an explicit configuration precedence only over a
// same-named managed installation. It never uses ordering to hide cross-owner
// claim conflicts.
func MergeRegistrations(managed, explicit []Registration) ([]Registration, error) {
	byName := make(map[string]Registration, len(managed)+len(explicit))
	for _, value := range managed {
		byName[value.Name] = value
	}
	for _, value := range explicit {
		byName[value.Name] = value
	}
	result := make([]Registration, 0, len(byName))
	for _, value := range byName {
		result = append(result, value)
	}
	slices.SortFunc(result, func(a, b Registration) int { return strings.Compare(a.Name, b.Name) })
	if err := ValidateClaimOverlap(result); err != nil {
		return nil, err
	}
	return result, nil
}

// ValidateClaimOverlap rejects two precise owners that can claim the same
// filename. Fallback registrations do not compete with precise registrations.
func ValidateClaimOverlap(registrations []Registration) error {
	for i := range registrations {
		for j := i + 1; j < len(registrations); j++ {
			a, b := registrations[i], registrations[j]
			if a.Claims.Fallback != b.Claims.Fallback {
				continue
			}
			if claim, ok := overlappingClaim(a.Claims.Inputs, b.Claims.Inputs); ok {
				// Disjoint conditional providers are allowed machine-wide. If a
				// worktree activates both marker sets, concrete routing below fails
				// closed and names the path. Two unconditional providers or providers
				// sharing an activation marker conflict wherever they participate.
				if !unconditionallyCoactive(a.Claims.Inputs, b.Claims.Inputs) {
					continue
				}
				return fmt.Errorf("adapter claim conflict: %q and %q both own %s", a.Name, b.Name, claim)
			}
		}
	}
	return nil
}

func unconditionallyCoactive(a, b Inputs) bool {
	if len(a.ProjectMarkers) == 0 && len(b.ProjectMarkers) == 0 {
		return true
	}
	for _, marker := range a.ProjectMarkers {
		if slices.Contains(b.ProjectMarkers, marker) {
			return true
		}
	}
	return false
}

func overlappingClaim(a, b Inputs) (string, bool) {
	if slices.Contains(a.Extensions, ".*") && len(b.Extensions)+len(b.Filenames) != 0 {
		return "extension .*", true
	}
	if slices.Contains(b.Extensions, ".*") && len(a.Extensions)+len(a.Filenames) != 0 {
		return "extension .*", true
	}
	for _, extension := range a.Extensions {
		if slices.Contains(b.Extensions, extension) {
			return "extension " + extension, true
		}
		for _, filename := range b.Filenames {
			if strings.EqualFold(filepath.Ext(filename), extension) {
				return "filename " + filename, true
			}
		}
	}
	for _, extension := range b.Extensions {
		for _, filename := range a.Filenames {
			if strings.EqualFold(filepath.Ext(filename), extension) {
				return "filename " + filename, true
			}
		}
	}
	for _, filename := range a.Filenames {
		if slices.Contains(b.Filenames, filename) {
			return "filename " + filename, true
		}
	}
	return "", false
}

// RouteInputs assigns concrete repository paths. Project markers activate a
// registration but are not themselves double-owned; precise owners take
// precedence over broad fallback adapters.
func RouteInputs(paths []string, registrations []Registration) (map[string][]string, error) {
	result := map[string][]string{}
	active := make(map[string]bool, len(registrations))
	for _, registration := range registrations {
		active[registration.Name] = claimsActive(paths, registration.Claims.Inputs)
	}
	for _, path := range paths {
		var precise, fallback []string
		for _, registration := range registrations {
			if !active[registration.Name] || !claimsPath(path, registration.Claims.Inputs) {
				continue
			}
			if registration.Claims.Fallback {
				fallback = append(fallback, registration.Name)
			} else {
				precise = append(precise, registration.Name)
			}
		}
		owners := precise
		if len(owners) == 0 {
			owners = fallback
		}
		slices.Sort(owners)
		if len(owners) > 1 {
			return nil, fmt.Errorf("adapter claim conflict: %q and %q both own path %s", owners[0], owners[1], filepath.ToSlash(path))
		}
		if len(owners) == 1 {
			result[owners[0]] = append(result[owners[0]], filepath.ToSlash(path))
		}
	}
	for name := range result {
		slices.Sort(result[name])
		result[name] = slices.Compact(result[name])
	}
	return result, nil
}

func claimsActive(paths []string, inputs Inputs) bool {
	if len(inputs.ProjectMarkers) == 0 {
		return true
	}
	return slices.ContainsFunc(paths, func(path string) bool {
		return slices.Contains(inputs.ProjectMarkers, strings.ToLower(filepath.Base(path)))
	})
}

func claimsPath(path string, inputs Inputs) bool {
	base, extension := strings.ToLower(filepath.Base(path)), strings.ToLower(filepath.Ext(path))
	return slices.Contains(inputs.Extensions, extension) || slices.Contains(inputs.Extensions, ".*") || slices.Contains(inputs.Filenames, base)
}
