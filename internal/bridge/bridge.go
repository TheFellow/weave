// Package bridge loads exact checked-in relationships that semantic compilers
// cannot establish across repository, language, schema, or generation boundaries.
package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
)

const (
	Schema       = "weave.bridges/v1"
	ProviderName = "weave-bridges"
	configPath   = ".weave/bridges.json"
	maxBytes     = 1 << 20
	maxLinks     = 4096
	endpointTag  = "symbol:"
)

// Config is the checked-in declaration document.
type Config struct {
	Schema string `json:"schema"`
	Links  []Link `json:"links"`
}

// Link is one exact, directed semantic relationship.
type Link struct {
	ID   string         `json:"id"`
	From string         `json:"from"`
	To   string         `json:"to"`
	Kind graph.EdgeKind `json:"kind"`
}

// Load parses and validates a bridge file. Missing files describe no links.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Schema: Schema}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open bridge configuration: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read bridge configuration: %w", err)
	}
	if len(content) > maxBytes {
		return Config{}, fmt.Errorf("bridge configuration exceeds %d bytes", maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode bridge configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, errors.New("decode bridge configuration: trailing JSON value")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate enforces the v1 exact endpoint and edge contract.
func (config Config) Validate() error {
	if config.Schema != Schema {
		return fmt.Errorf("bridge schema is %q, want %q", config.Schema, Schema)
	}
	if len(config.Links) > maxLinks {
		return fmt.Errorf("bridge configuration exceeds %d links", maxLinks)
	}
	seen := make(map[string]bool, len(config.Links))
	for _, link := range config.Links {
		if link.ID == "" || strings.ContainsRune(link.ID, 0) || seen[link.ID] {
			return fmt.Errorf("bridge link ID %q is empty, invalid, or duplicated", link.ID)
		}
		seen[link.ID] = true
		if _, err := endpoint(link.From); err != nil {
			return fmt.Errorf("bridge %q from: %w", link.ID, err)
		}
		if _, err := endpoint(link.To); err != nil {
			return fmt.Errorf("bridge %q to: %w", link.ID, err)
		}
		if !slices.Contains([]graph.EdgeKind{graph.EdgeDependsOn, graph.EdgeDocuments, graph.EdgeGenerates}, link.Kind) {
			return fmt.Errorf("bridge %q kind %q is not one of depends-on, documents, generates", link.ID, link.Kind)
		}
	}
	return nil
}

func endpoint(value string) (string, error) {
	if !strings.HasPrefix(value, endpointTag) || len(value) == len(endpointTag) {
		return "", fmt.Errorf("endpoint %q must use symbol:<exact-symbol-id>", value)
	}
	value = strings.TrimPrefix(value, endpointTag)
	if strings.ContainsRune(value, 0) || len(value) > 1<<20 {
		return "", fmt.Errorf("symbol endpoint is invalid or exceeds 1 MiB")
	}
	return value, nil
}

// Provider makes bridge configuration part of query-driven Git freshness.
type Provider struct{}

func (Provider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: ProviderName, Version: "1"}
}

func (Provider) Refresh(_ context.Context, request freshness.Request) (freshness.Result, error) {
	path := filepath.Join(request.Repository.Root, filepath.FromSlash(configPath))
	config, err := Load(path)
	if err != nil {
		return freshness.Result{}, err
	}
	previous := previousUnits(request.Previous)
	if len(config.Links) == 0 {
		return freshness.Result{Removed: sortedKeys(previous), Units: []freshness.Unit{}}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return freshness.Result{}, fmt.Errorf("read bridge fingerprint: %w", err)
	}
	digest := sha256.Sum256(content)
	fingerprint := "sha256:" + hex.EncodeToString(digest[:])
	unitID := "weave:bridges:" + shortHash(request.Repository.Identity)
	if !request.Force {
		if old, ok := previous[unitID]; ok && old.InputFingerprint == fingerprint {
			return freshness.Result{Units: []freshness.Unit{old}}, nil
		}
	}
	edges := make([]graph.Edge, 0, len(config.Links))
	for _, link := range config.Links {
		from, _ := endpoint(link.From)
		to, _ := endpoint(link.To)
		evidence := graph.EvidenceDeclared
		if link.Kind == graph.EdgeGenerates {
			evidence = graph.EvidenceGenerated
		}
		edges = append(edges, graph.Edge{
			ID: "bridge:" + shortHash(request.Repository.Identity+"\x00"+link.ID), UnitID: unitID,
			From: from, To: to, Kind: link.Kind, Evidence: evidence, Provider: ProviderName,
		})
	}
	slices.SortFunc(edges, graph.CompareEdges)
	facts := graph.UnitFacts{Unit: graph.Unit{
		ID: unitID, Provider: ProviderName, ProviderVersion: "1", InputFingerprint: fingerprint,
		InventoryDigest: fingerprint,
	}, Edges: edges}
	return freshness.Result{
		Batches: []graph.UnitFacts{facts}, Removed: removedExcept(previous, unitID),
		Units: []freshness.Unit{{ID: unitID, InputFingerprint: fingerprint, InventoryDigest: fingerprint}},
	}, nil
}

func shortHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func previousUnits(manifest *freshness.Manifest) map[string]freshness.Unit {
	result := map[string]freshness.Unit{}
	if manifest != nil {
		for _, unit := range manifest.Units {
			result[unit.ID] = unit
		}
	}
	return result
}

func sortedKeys(values map[string]freshness.Unit) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func removedExcept(values map[string]freshness.Unit, keep string) []string {
	delete(values, keep)
	return sortedKeys(values)
}
