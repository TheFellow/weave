// Package ci derives deterministic identifiers for disposable CI graph state.
package ci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/repository"
)

const Schema = "weave.ci/v1"

type Status struct {
	Schema       string               `json:"schema"`
	CacheKey     string               `json:"cache_key"`
	Repository   string               `json:"repository"`
	Commit       string               `json:"commit,omitempty"`
	Tree         string               `json:"tree,omitempty"`
	Dirty        bool                 `json:"dirty"`
	Provider     freshness.ProviderID `json:"provider"`
	GraphSchema  uint32               `json:"graph_schema"`
	ConfigDigest string               `json:"config_digest"`
}

// Key includes all repository contents via Git tree/overlay plus provider,
// normalized schema, platform, and checked-in policy configuration.
func Key(repo repository.Repository, state repository.State, provider freshness.ProviderID, configPath string) (Status, error) {
	configDigest := "none"
	if content, err := os.ReadFile(configPath); err == nil {
		digest := sha256.Sum256(content)
		configDigest = "sha256:" + hex.EncodeToString(digest[:])
	} else if !os.IsNotExist(err) {
		return Status{}, fmt.Errorf("hash CI configuration: %w", err)
	}
	payload := struct {
		Repository string
		Commit     string
		Tree       string
		Changes    []repository.Change
		Provider   freshness.ProviderID
		Schema     uint32
		OS         string
		Arch       string
		Config     string
	}{repo.Identity, state.Commit, state.Tree, state.Changes, provider, graph.SchemaVersion, runtime.GOOS, runtime.GOARCH, configDigest}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Status{}, err
	}
	digest := sha256.Sum256(encoded)
	return Status{
		Schema: Schema, CacheKey: "weave-v1-" + runtime.GOOS + "-" + runtime.GOARCH + "-" + hex.EncodeToString(digest[:16]),
		Repository: repo.Identity, Commit: state.Commit, Tree: state.Tree,
		Dirty: len(state.Changes) != 0, Provider: provider,
		GraphSchema: graph.SchemaVersion, ConfigDigest: configDigest,
	}, nil
}
