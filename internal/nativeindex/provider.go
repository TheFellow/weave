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
	"github.com/TheFellow/weave/internal/workspaceindex"
)

const maxInputBytes = 512 << 20
const maxAdapterOutputBytes = 256 << 20
const maxGitInventoryBytes = 16 << 20

// Default returns the automatic provider set for a worktree. Third-party
// registrations have already crossed the explicit registry trust boundary;
// Weave never infers them by scanning PATH.
func Default(directory string, registrations ...adapter.Registration) freshness.Provider {
	providers := []freshness.Provider{workspaceindex.Provider{}, goindex.Provider{}, bridge.Provider{}}
	candidates := []Provider{
		{Name: "weave-dotnet", Path: configuredPath("WEAVE_DOTNET_ADAPTER", "weave-dotnet"), Directory: directory, Profile: DotNetInputs, Permissions: adapter.Permissions{BuildTool: true}},
		{Name: "weave-python", Path: configuredPath("WEAVE_PYTHON_ADAPTER", "weave-python"), Directory: directory, Profile: PythonInputs, ProbeProviderVersion: true},
		{
			Name: "weave-rust", Path: configuredPath("WEAVE_RUST_ADAPTER", "weave-rust"), Directory: directory,
			Profile: RustInputs, Activation: adapter.Inputs{Filenames: []string{"cargo.toml", "rust-project.json"}},
			Permissions: adapter.Permissions{BuildTool: true}, ProbeProviderVersion: true, ConfigFingerprint: "builtin/rust-inputs/v1",
		},
		{
			Name: "scip:scip-clang", Path: configuredPath("WEAVE_CPP_ADAPTER", "weave-cpp"), Directory: directory,
			Profile: CppInputs, Activation: adapter.Inputs{Filenames: []string{"compile_commands.json"}},
			Permissions: adapter.Permissions{BuildTool: true}, ProbeProviderVersion: true, ConfigFingerprint: "builtin/cpp-inputs/v1",
		},
	}
	configured := append([]adapter.Registration(nil), registrations...)
	slices.SortFunc(configured, func(a, b adapter.Registration) int { return strings.Compare(a.Name, b.Name) })
	for _, registration := range configured {
		if len(registration.Command) == 0 {
			continue
		}
		candidates = append(candidates, Provider{
			Name: registration.Name, Path: registration.Command[0], Args: append([]string(nil), registration.Command[1:]...),
			Directory: directory, Profile: InputProfile(registration.Name), Inputs: registration.Inputs, Permissions: registration.Permissions,
			ProbeProviderVersion: true, Timeout: registration.Timeout, ConfigFingerprint: registration.ConfigFingerprint,
			Required: true,
		})
	}
	for _, provider := range candidates {
		if path, err := exec.LookPath(provider.Path); err == nil {
			if absolute, absoluteErr := filepath.Abs(path); absoluteErr == nil {
				path = absolute
			}
			provider.Path = path
			providers = append(providers, provider)
		} else if provider.Required {
			providers = append(providers, unavailableProvider(provider, err))
		}
	}
	return freshness.CompositeProvider{Providers: providers}
}

func configuredPath(environment, fallback string) string {
	if configured := os.Getenv(environment); configured != "" {
		return configured
	}
	return fallback
}

// InputProfile selects the repository inputs that invalidate an adapter.
type InputProfile string

const (
	DotNetInputs InputProfile = "dotnet"
	PythonInputs InputProfile = "python"
	RustInputs   InputProfile = "rust"
	CppInputs    InputProfile = "cpp"
)

// Provider invokes one compiler-native full-refresh adapter when its inputs change.
type Provider struct {
	Name                 string
	Path                 string
	Args                 []string
	Directory            string
	Profile              InputProfile
	Inputs               adapter.Inputs
	Activation           adapter.Inputs
	Permissions          adapter.Permissions
	ProbeProviderVersion bool
	ConfigFingerprint    string
	Required             bool
	Runner               adapter.Runner
	Timeout              time.Duration
}

func (provider Provider) ID() freshness.ProviderID {
	digest := sha256.Sum256([]byte(filepath.Clean(provider.Path)))
	if file, err := os.Open(provider.Path); err == nil {
		hash := sha256.New()
		_, _ = io.Copy(hash, io.LimitReader(file, 128<<20))
		_ = file.Close()
		copy(digest[:], hash.Sum(nil))
	}
	if provider.ConfigFingerprint != "" {
		configured := sha256.Sum256([]byte(hex.EncodeToString(digest[:]) + "\x00" + provider.ConfigFingerprint))
		digest = configured
	}
	return freshness.ProviderID{Name: "native/" + provider.name(), Version: "1." + hex.EncodeToString(digest[:6])}
}

func (provider Provider) Refresh(ctx context.Context, request freshness.Request) (freshness.Result, error) {
	paths, fingerprint, err := semanticInputsForActivation(ctx, request.Repository.Root, provider.profile(), provider.Inputs, provider.Activation)
	if err != nil {
		return freshness.Result{}, err
	}
	previous := previousUnits(request.Previous)
	if len(paths) == 0 || !provider.active(paths) {
		return freshness.Result{Removed: sortedKeys(previous), Units: []freshness.Unit{}}, nil
	}
	if provider.ProbeProviderVersion {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		capabilities, _, probeErr := provider.Runner.Describe(probeCtx, adapter.Executable{
			Path: provider.Path, Args: provider.Args, Dir: request.Repository.Root, Env: adapterEnvironment(),
		})
		cancel()
		if probeErr != nil {
			return freshness.Result{}, probeErr
		}
		if capabilities.Provider.Name != provider.name() {
			return freshness.Result{}, fmt.Errorf("adapter provider is %q, want %q", capabilities.Provider.Name, provider.name())
		}
		fingerprint += "\x00" + capabilities.Provider.Name + "\x00" + capabilities.Provider.Version
	}
	fingerprint = providerFingerprint(provider.ID(), fingerprint)
	if !request.Force && len(previous) != 0 && inventoryFingerprint(previous) == fingerprint {
		return freshness.Result{Units: sortedUnits(previous)}, nil
	}
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 4 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	requestID := make([]byte, 16)
	if _, err := rand.Read(requestID); err != nil {
		return freshness.Result{}, fmt.Errorf("create adapter request ID: %w", err)
	}
	runner := provider.Runner
	if runner.Limits.MaxTotalBytes == 0 {
		runner.Limits.MaxTotalBytes = maxAdapterOutputBytes
	}
	result, err := runner.Index(runCtx, adapter.Executable{
		Path: provider.Path, Args: provider.Args, Dir: request.Repository.Root, Env: adapterEnvironment(),
	}, adapter.IndexRequest{
		RequestID: hex.EncodeToString(requestID), RepositoryRoot: request.Repository.Root,
		RepositoryIdentity: request.Repository.Identity,
		ChangedPaths:       changedSemanticPathsFor(request.State.Changes, provider.profile(), provider.Inputs),
		Permissions:        provider.permissions(),
	})
	if err != nil {
		return freshness.Result{}, err
	}
	if provider.Name != "" && result.Provider.Name != provider.name() {
		return freshness.Result{}, fmt.Errorf("adapter provider is %q, want %q", result.Provider.Name, provider.name())
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

func (provider Provider) name() string {
	if provider.Name != "" {
		return provider.Name
	}
	return "weave-dotnet"
}

func (provider Provider) profile() InputProfile {
	if provider.Profile != "" {
		return provider.Profile
	}
	return DotNetInputs
}

func (provider Provider) permissions() adapter.Permissions {
	if provider.Permissions != (adapter.Permissions{}) || provider.profile() != DotNetInputs || hasConfiguredInputs(provider.Inputs) {
		return provider.Permissions
	}
	return adapter.Permissions{BuildTool: true}
}

func (provider Provider) active(paths []string) bool {
	if !hasConfiguredInputs(provider.Activation) {
		return true
	}
	return slices.ContainsFunc(paths, func(path string) bool {
		return isSemanticInputFor(path, provider.Profile, provider.Activation)
	})
}

func providerFingerprint(id freshness.ProviderID, inputs string) string {
	digest := sha256.Sum256([]byte(id.Name + "\x00" + id.Version + "\x00" + inputs))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func semanticInputs(ctx context.Context, root string, profile InputProfile) ([]string, string, error) {
	return semanticInputsFor(ctx, root, profile, adapter.Inputs{})
}

func semanticInputsFor(ctx context.Context, root string, profile InputProfile, inputs adapter.Inputs) ([]string, string, error) {
	return semanticInputsForActivation(ctx, root, profile, inputs, adapter.Inputs{})
}

func semanticInputsForActivation(ctx context.Context, root string, profile InputProfile, inputs, activation adapter.Inputs) ([]string, string, error) {
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "ls-files", "-co", "--exclude-standard", "-z", "--")
	command.Dir = root
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = maxGitInventoryBytes, 64<<10
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if stdout.exceeded {
		return nil, "", fmt.Errorf("Git semantic input inventory exceeds %d bytes", maxGitInventoryBytes)
	}
	if stderr.exceeded {
		return nil, "", fmt.Errorf("Git semantic input diagnostics exceed %d bytes", stderr.limit)
	}
	if err != nil {
		return nil, "", fmt.Errorf("list %s semantic inputs: %w: %s", profile, err, strings.TrimSpace(stderr.String()))
	}
	var paths []string
	for _, raw := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		path := filepath.ToSlash(string(raw))
		if isSemanticInputFor(path, profile, inputs) {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	if hasConfiguredInputs(activation) && !slices.ContainsFunc(paths, func(path string) bool {
		return isSemanticInputFor(path, profile, activation)
	}) {
		return nil, "", nil
	}
	hash := sha256.New()
	var total int64
	for _, path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, "", fmt.Errorf("inspect %s input %q: %w", profile, path, err)
		}
		if (profile == PythonInputs || profile == RustInputs || profile == CppInputs || hasConfiguredInputs(inputs)) && info.Mode()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("%s source is a symlink: %s", profile, path)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
		if total > maxInputBytes {
			return nil, "", fmt.Errorf("%s semantic inputs exceed %d bytes", profile, maxInputBytes)
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return nil, "", fmt.Errorf("read %s input %q: %w", profile, path, err)
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return paths, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - buffer.Len()
	if remaining < len(value) {
		buffer.exceeded = true
		if remaining <= 0 {
			return original, nil
		}
		value = value[:remaining]
	}
	_, _ = buffer.Buffer.Write(value)
	return original, nil
}

func isSemanticInput(path string, profile InputProfile) bool {
	return isSemanticInputFor(path, profile, adapter.Inputs{})
}

func isSemanticInputFor(path string, profile InputProfile, inputs adapter.Inputs) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	// Rust include macros and C-family preprocessor/compiler flags can consume
	// tracked files with arbitrary extensions. Until an adapter returns an exact
	// dependency inventory, hash every Git-visible file once its project root
	// activates the provider.
	if (profile == RustInputs || profile == CppInputs) && !hasConfiguredInputs(inputs) {
		return true
	}
	if hasConfiguredInputs(inputs) {
		return slices.Contains(inputs.Extensions, ext) || slices.Contains(inputs.Filenames, base)
	}
	if profile == PythonInputs {
		return ext == ".py"
	}
	if slices.Contains([]string{".cs", ".csx", ".fs", ".fsx", ".csproj", ".fsproj", ".sln", ".slnx", ".props", ".targets"}, ext) {
		return true
	}
	return base == "global.json" || base == "nuget.config" || base == "packages.lock.json" ||
		base == ".editorconfig" || base == "directory.build.props" || base == "directory.build.targets" || base == "directory.packages.props"
}

func changedSemanticPaths(changes []repository.Change, profile InputProfile) []string {
	return changedSemanticPathsFor(changes, profile, adapter.Inputs{})
}

func changedSemanticPathsFor(changes []repository.Change, profile InputProfile, inputs adapter.Inputs) []string {
	var result []string
	for _, change := range changes {
		path := change.Path
		if isSemanticInputFor(path, profile, inputs) {
			result = append(result, filepath.ToSlash(path))
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func hasConfiguredInputs(inputs adapter.Inputs) bool {
	return len(inputs.Extensions)+len(inputs.Filenames) != 0
}

type configurationErrorProvider struct {
	err error
	id  freshness.ProviderID
}

// ConfigurationError returns a provider that fails every attempted refresh.
// It lets adapter inspection report a malformed registry while automatic
// freshness remains fail-closed.
func ConfigurationError(err error) freshness.Provider {
	digest := sha256.Sum256([]byte(err.Error()))
	return configurationErrorProvider{err: err, id: freshness.ProviderID{
		Name: "native/adapter-configuration-error", Version: "1." + hex.EncodeToString(digest[:6]),
	}}
}

func unavailableProvider(configured Provider, lookupErr error) freshness.Provider {
	digest := sha256.Sum256([]byte(configured.ConfigFingerprint + "\x00" + configured.Path))
	return configurationErrorProvider{
		err: fmt.Errorf("find registered adapter %q command %q: %w", configured.name(), configured.Path, lookupErr),
		id:  freshness.ProviderID{Name: "native/" + configured.name(), Version: "1." + hex.EncodeToString(digest[:6])},
	}
}

func (provider configurationErrorProvider) ID() freshness.ProviderID { return provider.id }

func (provider configurationErrorProvider) Refresh(context.Context, freshness.Request) (freshness.Result, error) {
	return freshness.Result{}, fmt.Errorf("load automatic adapter registry: %w", provider.err)
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
	allowed := []string{"PATH", "DOTNET_ROOT", "DOTNET_HOST_PATH", "CARGO_HOME", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN", "WEAVE_RUST_ANALYZER", "WEAVE_SCIP_CLANG", "TMPDIR", "TMP", "TEMP", "SystemRoot", "WINDIR", "HOME", "USERPROFILE", "NUGET_PACKAGES"}
	var environment []string
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
