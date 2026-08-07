// Package scipimport normalizes bounded SCIP protobuf indexes into Weave facts.
package scipimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// Limits bound protobuf and normalized graph materialization.
type Limits struct {
	MaxIndexBytes  int64
	MaxSourceBytes int64
	MaxDocuments   int
	MaxFacts       int
	MaxStringBytes int
	ProtobufDepth  int
}

func (limits Limits) withDefaults() Limits {
	if limits.MaxIndexBytes <= 0 {
		limits.MaxIndexBytes = 64 << 20
	}
	if limits.MaxSourceBytes <= 0 {
		limits.MaxSourceBytes = 16 << 20
	}
	if limits.MaxDocuments <= 0 {
		limits.MaxDocuments = 100_000
	}
	if limits.MaxFacts <= 0 {
		limits.MaxFacts = 2_000_000
	}
	if limits.MaxStringBytes <= 0 {
		limits.MaxStringBytes = 1 << 20
	}
	if limits.ProtobufDepth <= 0 {
		limits.ProtobufDepth = 100
	}
	return limits
}

// Options identify the local source tree into which SCIP paths resolve.
type Options struct {
	RepositoryRoot     string
	RepositoryIdentity string
	// LegacyPositionEncoding is an explicit producer-specific compatibility
	// override for documents created before SCIP required position_encoding.
	// It must not be inferred from Metadata.text_document_encoding: that field
	// describes source bytes on disk, not range character units.
	LegacyPositionEncoding scip.PositionEncoding
}

// Importer reads SCIP without invoking its producer.
type Importer struct{ Limits Limits }

// Result is one complete producer inventory. Provider is the stable
// replacement scope; ProviderVersion records the exact producer release.
type Result struct {
	Provider        string
	ProviderVersion string
	Units           []graph.UnitFacts
}

// ImportFile bounds and imports one explicitly selected .scip file.
func (importer Importer) ImportFile(ctx context.Context, path string, options Options) (Result, error) {
	limits := importer.Limits.withDefaults()
	file, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open SCIP index: %w", err)
	}
	defer file.Close()
	data, err := readBounded(file, limits.MaxIndexBytes)
	if err != nil {
		return Result{}, fmt.Errorf("read SCIP index: %w", err)
	}
	return importer.Import(ctx, data, options)
}

// Import decodes and completely validates one SCIP protobuf before returning facts.
func (importer Importer) Import(ctx context.Context, data []byte, options Options) (Result, error) {
	limits := importer.Limits.withDefaults()
	if int64(len(data)) > limits.MaxIndexBytes {
		return Result{}, errors.New("SCIP index exceeds configured byte limit")
	}
	root, err := canonicalRoot(options.RepositoryRoot)
	if err != nil {
		return Result{}, err
	}
	identity := options.RepositoryIdentity
	if identity == "" {
		identity = filepath.ToSlash(root)
	}
	var index scip.Index
	if err := (proto.UnmarshalOptions{RecursionLimit: limits.ProtobufDepth}).Unmarshal(data, &index); err != nil {
		return Result{}, fmt.Errorf("decode SCIP protobuf: %w", err)
	}
	if index.Metadata == nil || index.Metadata.ToolInfo == nil || index.Metadata.ToolInfo.Name == "" || index.Metadata.ToolInfo.Version == "" {
		return Result{}, errors.New("SCIP metadata tool name and version are required")
	}
	if len(index.Documents) > limits.MaxDocuments {
		return Result{}, errors.New("SCIP document count exceeds configured limit")
	}
	provider := "scip:" + index.Metadata.ToolInfo.Name
	providerVersion := index.Metadata.ToolInfo.Version
	paths := map[string]bool{}
	units := make([]graph.UnitFacts, 0, len(index.Documents))
	totalFacts := 0
	for number, document := range index.Documents {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if document == nil {
			return Result{}, fmt.Errorf("SCIP document %d is nil", number)
		}
		path, localPath, err := safeDocumentPath(document.RelativePath)
		if err != nil {
			return Result{}, fmt.Errorf("SCIP document %d: %w", number, err)
		}
		if paths[path] {
			return Result{}, fmt.Errorf("duplicate SCIP document path %q", path)
		}
		paths[path] = true
		source, err := documentSource(root, localPath, document.Text, limits.MaxSourceBytes)
		if err != nil {
			return Result{}, fmt.Errorf("SCIP document %q: %w", path, err)
		}
		positionEncoding := document.PositionEncoding
		if positionEncoding == scip.PositionEncoding_UnspecifiedPositionEncoding {
			positionEncoding = options.LegacyPositionEncoding
		}
		facts, err := normalizeDocument(document, source, identity, path, provider, providerVersion, positionEncoding, limits)
		if err != nil {
			return Result{}, fmt.Errorf("SCIP document %q: %w", path, err)
		}
		totalFacts += len(facts.Documents) + len(facts.Symbols) + len(facts.Occurrences) + len(facts.Edges)
		if totalFacts > limits.MaxFacts {
			return Result{}, errors.New("SCIP fact count exceeds configured limit")
		}
		units = append(units, facts)
	}
	slices.SortFunc(units, func(a, b graph.UnitFacts) int { return strings.Compare(a.Unit.ID, b.Unit.ID) })
	if err := deduplicateGlobalSymbols(units); err != nil {
		return Result{}, err
	}
	if err := validateGlobalIDs(units); err != nil {
		return Result{}, err
	}
	return Result{Provider: provider, ProviderVersion: providerVersion, Units: units}, nil
}

func canonicalRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("repository root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository root is not a directory")
	}
	return resolved, nil
}

func safeDocumentPath(path string) (string, string, error) {
	if path == "" || strings.Contains(path, "\\") || !fs.ValidPath(path) {
		return "", "", fmt.Errorf("document path %q is not canonical repository-relative slash path", path)
	}
	local, err := filepath.Localize(path)
	if err != nil || !filepath.IsLocal(local) {
		return "", "", fmt.Errorf("document path %q is not local", path)
	}
	return path, local, nil
}

func documentSource(root, localPath, embedded string, maximum int64) ([]byte, error) {
	target := filepath.Join(root, localPath)
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("document path is a symbolic link")
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("document path is not a regular file")
		}
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil {
			return nil, fmt.Errorf("resolve document path: %w", err)
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || !filepath.IsLocal(relative) {
			return nil, errors.New("document path escapes repository through symlinks")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect document path: %w", err)
	}
	if embedded != "" {
		if int64(len(embedded)) > maximum {
			return nil, errors.New("embedded source exceeds configured byte limit")
		}
		return []byte(embedded), nil
	}
	if err != nil {
		return nil, errors.New("document source is absent from SCIP and repository")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("open document source: %w", err)
	}
	defer file.Close()
	value, err := readBounded(file, maximum)
	if err != nil {
		return nil, fmt.Errorf("read document source: %w", err)
	}
	return value, nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > maximum {
		return nil, errors.New("input exceeds configured byte limit")
	}
	return value, nil
}

func stableID(prefix string, parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(part))
	}
	return prefix + hex.EncodeToString(hash.Sum(nil))
}
