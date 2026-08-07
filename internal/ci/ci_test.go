package ci_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TheFellow/weave/internal/ci"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/repository"
)

func TestCacheKeyIsDeterministicAndInputSensitive(t *testing.T) {
	repo := repository.Repository{Identity: "github.com/acme/app"}
	state := repository.State{Commit: "commit", Tree: "tree"}
	provider := freshness.ProviderID{Name: "fixture", Version: "1"}
	config := filepath.Join(t.TempDir(), "architecture.json")
	if err := os.WriteFile(config, []byte("policy-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := ci.Key(repo, state, provider, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ci.Key(repo, state, provider, config)
	if err != nil || first != second {
		t.Fatalf("second = %#v, %v; first = %#v", second, err, first)
	}
	state.Changes = []repository.Change{{Kind: '?', Path: "new.go", ContentHash: "sha256:1"}}
	dirty, err := ci.Key(repo, state, provider, config)
	if err != nil || dirty.CacheKey == first.CacheKey || !dirty.Dirty {
		t.Fatalf("dirty = %#v, %v", dirty, err)
	}
	if err := os.WriteFile(config, []byte("policy-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ci.Key(repo, repository.State{Commit: "commit", Tree: "tree"}, provider, config)
	if err != nil || changed.CacheKey == first.CacheKey {
		t.Fatalf("changed = %#v, %v", changed, err)
	}
}
