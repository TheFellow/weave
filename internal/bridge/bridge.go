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
	"time"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/processlock"
	"github.com/TheFellow/weave/internal/relationship"
)

const (
	Schema          = "weave.bridges/v1"
	ProviderName    = "weave-bridges"
	providerVersion = "2"
	configPath      = ".weave/bridges.json"
	maxBytes        = 1 << 20
	maxLinks        = 4096
	endpointTag     = "entity:"
	legacyTag       = "symbol:"
	maxNoteBytes    = 8 << 10
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
	Note string         `json:"note,omitempty"`
}

// Path returns the repository-relative declaration path rooted at root.
func Path(root string) string {
	return filepath.Join(root, filepath.FromSlash(configPath))
}

// Entity stores an exact normalized graph endpoint in a declaration.
func Entity(id string) string { return endpointTag + id }

// Endpoint returns the exact normalized graph ID from a declaration endpoint.
// The symbol tag remains accepted for v1 files written before heterogeneous
// workspace resources were authorable.
func Endpoint(value string) (string, error) { return endpoint(value) }

// Revision returns an order-independent digest of one validated declaration
// document. Interactive and other long-lived clients use it for optimistic
// concurrency; it is not a semantic graph generation or a storage identity.
func Revision(config Config) (string, error) {
	config.Links = append([]Link(nil), config.Links...)
	slices.SortFunc(config.Links, func(a, b Link) int { return strings.Compare(a.ID, b.ID) })
	if err := config.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode bridge revision: %w", err)
	}
	digest := sha256.Sum256(append([]byte("weave.bridges-revision/v1\x00"), encoded...))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
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

// Edit serializes one read-modify-write operation with a lock database kept in
// Git-private derived storage. The callback runs only after the latest source
// declaration has been loaded under the lock.
func Edit(ctx context.Context, path, lockPath string, edit func(*Config) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lockPath == "" {
		return errors.New("bridge edit lock path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("create bridge edit lock directory: %w", err)
	}
	lock, err := processlock.Acquire(ctx, lockPath, 0o600, 2*time.Second)
	if err != nil {
		return fmt.Errorf("acquire bridge edit lock: %w", err)
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	config, err := Load(path)
	if err != nil {
		return err
	}
	if err := edit(&config); err != nil {
		return err
	}
	return Save(path, config)
}

// Save validates and atomically writes a canonical declaration file.
func Save(path string, config Config) error {
	config.Links = append([]Link(nil), config.Links...)
	slices.SortFunc(config.Links, func(a, b Link) int { return strings.Compare(a.ID, b.ID) })
	if err := config.Validate(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to replace symlinked bridge configuration")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect bridge configuration: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create bridge configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".bridges-*.json")
	if err != nil {
		return fmt.Errorf("create temporary bridge configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set bridge configuration permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode bridge configuration: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync bridge configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close bridge configuration: %w", err)
	}
	if info, err := os.Stat(temporaryPath); err != nil {
		return fmt.Errorf("inspect encoded bridge configuration: %w", err)
	} else if info.Size() > maxBytes {
		return fmt.Errorf("bridge configuration exceeds %d bytes", maxBytes)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish bridge configuration: %w", err)
	}
	return nil
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
		if link.ID == "" || len(link.ID) > 256 || !utf8.ValidString(link.ID) || strings.ContainsRune(link.ID, 0) || seen[link.ID] {
			return fmt.Errorf("bridge link ID %q is empty, invalid, or duplicated", link.ID)
		}
		seen[link.ID] = true
		if _, err := endpoint(link.From); err != nil {
			return fmt.Errorf("bridge %q from: %w", link.ID, err)
		}
		if _, err := endpoint(link.To); err != nil {
			return fmt.Errorf("bridge %q to: %w", link.ID, err)
		}
		if !graph.IsEdgeKind(link.Kind) {
			return fmt.Errorf("bridge %q has unknown relationship kind %q", link.ID, link.Kind)
		}
		if len(link.Note) > maxNoteBytes || !utf8.ValidString(link.Note) || strings.ContainsRune(link.Note, 0) {
			return fmt.Errorf("bridge %q note is invalid or exceeds %d bytes", link.ID, maxNoteBytes)
		}
	}
	return nil
}

func endpoint(value string) (string, error) {
	tag := endpointTag
	if strings.HasPrefix(value, legacyTag) {
		tag = legacyTag
	}
	if !strings.HasPrefix(value, tag) || len(value) == len(tag) {
		return "", fmt.Errorf("endpoint %q must use entity:<exact-graph-id>", value)
	}
	value = strings.TrimPrefix(value, tag)
	if strings.ContainsRune(value, 0) || !utf8.ValidString(value) || len(value) > 1<<20 {
		return "", fmt.Errorf("entity endpoint is invalid or exceeds 1 MiB")
	}
	return value, nil
}

// Provider makes bridge configuration part of query-driven Git freshness.
type Provider struct{}

func (Provider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: ProviderName, Version: providerVersion}
}

func (Provider) Refresh(_ context.Context, request freshness.Request) (freshness.Result, error) {
	path := Path(request.Repository.Root)
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
	builder := relationship.Builder{UnitID: unitID, Provider: ProviderName, Evidence: graph.EvidenceDeclared}
	for _, link := range config.Links {
		from, _ := endpoint(link.From)
		to, _ := endpoint(link.To)
		evidence := graph.EvidenceDeclared
		if link.Kind == graph.EdgeGenerates {
			evidence = graph.EvidenceGenerated
		}
		edge, err := builder.Build(relationship.Spec{
			ID:   "bridge:" + shortHash(request.Repository.Identity+"\x00"+link.ID),
			From: from, To: to, Kind: link.Kind, Evidence: evidence,
		})
		if err != nil {
			return freshness.Result{}, fmt.Errorf("build bridge %q: %w", link.ID, err)
		}
		edges = append(edges, edge)
	}
	slices.SortFunc(edges, graph.CompareEdges)
	facts := graph.UnitFacts{Unit: graph.Unit{
		ID: unitID, Provider: ProviderName, ProviderVersion: providerVersion, InputFingerprint: fingerprint,
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
