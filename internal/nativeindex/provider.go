// Package nativeindex integrates discovered compiler-native adapters with Git freshness.
package nativeindex

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/goindex"
	"github.com/TheFellow/weave/internal/repository"
)

const maxInputBytes = 512 << 20

// Default returns the automatic provider set for a worktree. Only the known
// weave-dotnet adapter is automatically trusted and executed.
func Default(directory string) freshness.Provider {
	providers := []freshness.Provider{goindex.Provider{}, bridge.Provider{}}
	configured := os.Getenv("WEAVE_DOTNET_ADAPTER")
	if configured == "" {
		configured = "weave-dotnet"
	}
	if path, err := exec.LookPath(configured); err == nil {
		if absolute, absoluteErr := filepath.Abs(path); absoluteErr == nil {
			path = absolute
		}
		providers = append(providers, Provider{Path: path, Directory: directory})
	}
	return freshness.CompositeProvider{Providers: providers}
}

// Provider invokes one weave-dotnet full-refresh only when its semantic inputs change.
type Provider struct {
	Path      string
	Args      []string
	Directory string
	Runner    adapter.Runner
	Timeout   time.Duration
}

func (provider Provider) ID() freshness.ProviderID {
	digest := sha256.Sum256([]byte(filepath.Clean(provider.Path)))
	if file, err := os.Open(provider.Path); err == nil {
		hash := sha256.New()
		_, _ = io.Copy(hash, io.LimitReader(file, 128<<20))
		_ = file.Close()
		copy(digest[:], hash.Sum(nil))
	}
	return freshness.ProviderID{Name: "native/weave-dotnet", Version: "1." + hex.EncodeToString(digest[:6])}
}

func (provider Provider) Refresh(ctx context.Context, request freshness.Request) (freshness.Result, error) {
	paths, fingerprint, err := semanticInputs(ctx, request.Repository.Root)
	if err != nil {
		return freshness.Result{}, err
	}
	fingerprint = providerFingerprint(provider.ID(), fingerprint)
	previous := previousUnits(request.Previous)
	if len(paths) == 0 {
		return freshness.Result{Removed: sortedKeys(previous), Units: []freshness.Unit{}}, nil
	}
	if !request.Force && len(previous) != 0 && inventoryFingerprint(previous) == fingerprint {
		return freshness.Result{Units: sortedUnits(previous)}, nil
	}
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	requestID := make([]byte, 16)
	if _, err := rand.Read(requestID); err != nil {
		return freshness.Result{}, fmt.Errorf("create adapter request ID: %w", err)
	}
	result, err := provider.Runner.Index(runCtx, adapter.Executable{
		Path: provider.Path, Args: provider.Args, Dir: request.Repository.Root, Env: adapterEnvironment(),
	}, adapter.IndexRequest{
		RequestID: hex.EncodeToString(requestID), RepositoryRoot: request.Repository.Root,
		RepositoryIdentity: request.Repository.Identity,
		ChangedPaths:       changedSemanticPaths(request.State.Changes),
		Permissions:        adapter.Permissions{BuildTool: true},
	})
	if err != nil {
		return freshness.Result{}, err
	}
	units := make([]freshness.Unit, 0, len(result.Units))
	present := make(map[string]bool, len(result.Units))
	for _, facts := range result.Units {
		present[facts.Unit.ID] = true
		units = append(units, freshness.Unit{
			ID: facts.Unit.ID, InputFingerprint: fingerprint,
			SurfaceFingerprint: facts.Unit.SurfaceFingerprint, InventoryDigest: facts.Unit.InventoryDigest,
		})
	}
	var removed []string
	for id := range previous {
		if !present[id] {
			removed = append(removed, id)
		}
	}
	slices.SortFunc(units, func(a, b freshness.Unit) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(removed)
	return freshness.Result{Batches: result.Units, Removed: removed, Units: units}, nil
}

func providerFingerprint(id freshness.ProviderID, inputs string) string {
	digest := sha256.Sum256([]byte(id.Name + "\x00" + id.Version + "\x00" + inputs))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func semanticInputs(ctx context.Context, root string) ([]string, string, error) {
	command := exec.CommandContext(ctx, "git", "ls-files", "-co", "--exclude-standard", "-z", "--")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, "", fmt.Errorf("list .NET semantic inputs: %w", err)
	}
	var paths []string
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		path := filepath.ToSlash(string(raw))
		if isSemanticInput(path) {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	hash := sha256.New()
	var total int64
	for _, path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, "", fmt.Errorf("inspect .NET input %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
		if total > maxInputBytes {
			return nil, "", fmt.Errorf(".NET semantic inputs exceed %d bytes", maxInputBytes)
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return nil, "", fmt.Errorf("read .NET input %q: %w", path, err)
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return paths, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func isSemanticInput(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	if slices.Contains([]string{".cs", ".csx", ".fs", ".fsx", ".csproj", ".fsproj", ".sln", ".slnx", ".props", ".targets"}, ext) {
		return true
	}
	return base == "global.json" || base == "nuget.config" || base == "packages.lock.json" ||
		base == ".editorconfig" || base == "directory.build.props" || base == "directory.build.targets" || base == "directory.packages.props"
}

func changedSemanticPaths(changes []repository.Change) []string {
	var result []string
	for _, change := range changes {
		path := change.Path
		if isSemanticInput(path) {
			result = append(result, filepath.ToSlash(path))
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
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

func inventoryFingerprint(units map[string]freshness.Unit) string {
	for _, unit := range units {
		return unit.InputFingerprint
	}
	return ""
}

func sortedUnits(values map[string]freshness.Unit) []freshness.Unit {
	result := make([]freshness.Unit, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	slices.SortFunc(result, func(a, b freshness.Unit) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func sortedKeys(values map[string]freshness.Unit) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

func adapterEnvironment() []string {
	allowed := []string{"PATH", "DOTNET_ROOT", "DOTNET_HOST_PATH", "TMPDIR", "TMP", "TEMP", "SystemRoot", "WINDIR", "HOME", "USERPROFILE", "NUGET_PACKAGES"}
	var environment []string
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
