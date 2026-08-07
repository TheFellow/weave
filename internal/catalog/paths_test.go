package catalog

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStateBasePlatformRules(t *testing.T) {
	environment := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	fallback := func() (string, error) { return `C:\Users\ryan\AppData\Roaming`, nil }
	tests := []struct {
		name, goos, home string
		environment      map[string]string
		want             string
	}{
		{"macOS application support", "darwin", "/Users/ryan", nil, "/Users/ryan/Library/Application Support"},
		{"Linux XDG state", "linux", "/home/ryan", map[string]string{"XDG_STATE_HOME": "/state"}, "/state"},
		{"Linux relative XDG ignored", "linux", "/home/ryan", map[string]string{"XDG_STATE_HOME": "relative"}, "/home/ryan/.local/state"},
		{"Windows local app data", "windows", `C:\Users\ryan`, map[string]string{"LOCALAPPDATA": `C:\Users\ryan\AppData\Local`}, `C:\Users\ryan\AppData\Local`},
		{"Windows config fallback", "windows", `C:\Users\ryan`, nil, `C:\Users\ryan\AppData\Roaming`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := stateBase(test.goos, test.home, environment(test.environment), fallback)
			if err != nil || got != test.want {
				t.Fatalf("stateBase = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDefaultAggregateDirectoryFollowsCatalogAndExplicitOverride(t *testing.T) {
	t.Setenv("WEAVE_AGGREGATE", "")
	root := t.TempDir()
	catalogPath := filepath.Join(root, "catalog.db")
	want := filepath.Join(root, "aggregate")
	if got, err := DefaultAggregateDirectory(catalogPath, ""); err != nil || got != want {
		t.Fatalf("DefaultAggregateDirectory = %q, %v; want %q", got, err, want)
	}
	override := filepath.Join(root, "custom-cache")
	if got, err := DefaultAggregateDirectory(catalogPath, override); err != nil || got != override {
		t.Fatalf("explicit aggregate = %q, %v", got, err)
	}
	if _, err := DefaultAggregateDirectory(catalogPath, "relative"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative aggregate error = %v", err)
	}
	t.Setenv("WEAVE_AGGREGATE", override)
	if got, err := DefaultAggregateDirectory(catalogPath, ""); err != nil || got != override {
		t.Fatalf("environment aggregate = %q, %v", got, err)
	}
}
