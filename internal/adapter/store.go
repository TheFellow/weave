package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/TheFellow/weave/internal/processlock"
)

const (
	InstallationSchema = "weave.adapter-installations/v1"
	maxManifestBytes   = 1 << 20
	maxArtifactBytes   = 512 << 20
)

// Installation is one locally managed executable and its pinned public
// contract. Artifact is relative to Store.Root and never interpreted as argv.
type Installation struct {
	Name             string       `json:"name"`
	Artifact         string       `json:"artifact"`
	Arguments        []string     `json:"arguments,omitempty"`
	ArtifactDigest   string       `json:"artifact_digest"`
	CapabilityDigest string       `json:"capability_digest"`
	Capabilities     Capabilities `json:"capabilities"`
	Permissions      Permissions  `json:"permissions,omitempty"`
	Timeout          string       `json:"timeout,omitempty"`
}

type installationManifest struct {
	Schema        string         `json:"schema"`
	Installations []Installation `json:"installations"`
}

// Store owns adapter artifacts and their atomic user-state manifest.
type Store struct {
	Root   string
	Runner Runner
}

func DefaultStore() (Store, error) {
	if configured := os.Getenv("WEAVE_ADAPTER_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return Store{}, errors.New("WEAVE_ADAPTER_HOME must be absolute")
		}
		return Store{Root: filepath.Clean(configured)}, nil
	}
	base, err := adapterStateBase(runtime.GOOS, os.UserHomeDir, os.Getenv, os.UserConfigDir)
	if err != nil {
		return Store{}, err
	}
	return Store{Root: filepath.Join(base, "weave", "adapters")}, nil
}

func adapterStateBase(goos string, home func() (string, error), getenv func(string) string, config func() (string, error)) (string, error) {
	switch goos {
	case "darwin":
		directory, err := home()
		if err != nil {
			return "", err
		}
		return path.Join(directory, "Library", "Application Support"), nil
	case "linux":
		if value := getenv("XDG_STATE_HOME"); path.IsAbs(value) {
			return value, nil
		}
		directory, err := home()
		if err != nil {
			return "", err
		}
		return path.Join(directory, ".local", "state"), nil
	case "windows":
		if value := getenv("LOCALAPPDATA"); filepath.IsAbs(value) {
			return value, nil
		}
	}
	return config()
}

func (store Store) manifestPath() string { return filepath.Join(store.Root, "manifest.json") }

func (store Store) withLock(ctx context.Context, action func() error) error {
	if store.Root == "" {
		return errors.New("adapter store root is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := os.Lstat(store.Root); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("adapter store root must be a non-symlink directory")
	}
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		return fmt.Errorf("create adapter store: %w", err)
	}
	lockPath := filepath.Join(store.Root, ".manifest.lock")
	if info, err := os.Lstat(lockPath); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("adapter manifest lock must be a regular non-symlink file")
	}
	lock, err := processlock.Acquire(ctx, lockPath, 0o600, 5*time.Second)
	if err != nil {
		return fmt.Errorf("lock adapter manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = lock.Close()
		return err
	}
	actionErr := action()
	return errors.Join(actionErr, lock.Close())
}

func (store Store) Load(ctx context.Context) ([]Installation, error) {
	if _, err := os.Stat(store.Root); os.IsNotExist(err) {
		return nil, nil
	}
	var result []Installation
	err := store.withLock(ctx, func() error {
		values, err := store.loadUnlocked()
		result = values
		return err
	})
	return result, err
}

func (store Store) loadUnlocked() ([]Installation, error) {
	if _, err := os.Stat(store.manifestPath()); os.IsNotExist(err) {
		backup := store.manifestPath() + ".previous"
		if _, backupErr := os.Stat(backup); backupErr == nil {
			if renameErr := os.Rename(backup, store.manifestPath()); renameErr != nil {
				return nil, fmt.Errorf("recover adapter manifest: %w", renameErr)
			}
		}
	}
	file, err := os.Open(store.manifestPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open adapter manifest: %w", err)
	}
	if info, statErr := file.Stat(); statErr != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("adapter manifest must be a regular file")
	}
	if info, lstatErr := os.Lstat(store.manifestPath()); lstatErr != nil || info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return nil, errors.New("adapter manifest must not be a symlink")
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read adapter manifest: %w", err)
	}
	if len(content) > maxManifestBytes {
		return nil, fmt.Errorf("adapter manifest exceeds %d bytes", maxManifestBytes)
	}
	var document installationManifest
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode adapter manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode adapter manifest: %w", err)
	}
	if document.Schema != InstallationSchema {
		return nil, fmt.Errorf("adapter manifest schema is %q, want %q", document.Schema, InstallationSchema)
	}
	if len(document.Installations) > maxRegistrations {
		return nil, errors.New("adapter manifest has too many installations")
	}
	seen := map[string]bool{}
	for i := range document.Installations {
		value := &document.Installations[i]
		if seen[value.Name] {
			return nil, fmt.Errorf("adapter manifest has duplicate name %q", value.Name)
		}
		seen[value.Name] = true
		if filepath.IsAbs(value.Artifact) || !filepath.IsLocal(value.Artifact) || filepath.Clean(value.Artifact) != value.Artifact {
			return nil, fmt.Errorf("adapter %q artifact is not a clean relative path", value.Name)
		}
		if filepath.Dir(value.Artifact) != "bin" || filepath.Base(value.Artifact) == "." || filepath.Base(value.Artifact) == ".." {
			return nil, fmt.Errorf("adapter %q artifact must be one file directly under bin", value.Name)
		}
		if !validDigest(value.ArtifactDigest) || !validDigest(value.CapabilityDigest) {
			return nil, fmt.Errorf("adapter %q has an invalid digest", value.Name)
		}
		if len(value.Arguments) > 63 {
			return nil, fmt.Errorf("adapter %q has too many arguments", value.Name)
		}
		for index, argument := range value.Arguments {
			if err := validRegistryText(fmt.Sprintf("adapter %q argument[%d]", value.Name, index), argument, 32<<10, true); err != nil {
				return nil, err
			}
		}
		if value.Timeout != "" {
			timeout, err := time.ParseDuration(value.Timeout)
			if err != nil || timeout <= 0 || timeout > time.Hour {
				return nil, fmt.Errorf("adapter %q has invalid timeout %q", value.Name, value.Timeout)
			}
		}
		normalized, err := NormalizeCapabilities(value.Capabilities)
		if err != nil {
			return nil, fmt.Errorf("adapter %q capabilities: %w", value.Name, err)
		}
		value.Capabilities = normalized
		digest, _ := CapabilityDigest(normalized)
		if digest != value.CapabilityDigest || normalized.Provider.Name != value.Name {
			return nil, fmt.Errorf("adapter %q capability pin is inconsistent", value.Name)
		}
	}
	slices.SortFunc(document.Installations, func(a, b Installation) int { return strings.Compare(a.Name, b.Name) })
	return document.Installations, nil
}

// Registrations converts pinned metadata into automatic providers without
// reading artifact bytes or executing anything. Doctor and automatic refresh
// perform integrity verification at their respective trust boundaries.
func (store Store) Registrations(ctx context.Context) ([]Registration, []Installation, error) {
	installed, err := store.Load(ctx)
	if err != nil {
		return nil, nil, err
	}
	registrations := make([]Registration, 0, len(installed))
	for _, item := range installed {
		path := filepath.Join(store.Root, item.Artifact)
		timeout, _ := time.ParseDuration(item.Timeout)
		command := append([]string{path}, item.Arguments...)
		registration := Registration{
			Name: item.Name, Command: command, Inputs: item.Capabilities.Claims.Inputs,
			Claims: item.Capabilities.Claims, Permissions: item.Permissions, Timeout: timeout,
			CapabilityDigest: item.CapabilityDigest, ArtifactDigest: item.ArtifactDigest,
			ConfigFingerprint: item.CapabilityDigest + "\x00" + item.ArtifactDigest, Source: "managed",
			PinnedCapabilities: &item.Capabilities,
		}
		registrations = append(registrations, registration)
	}
	return registrations, installed, nil
}

// VerifyArtifact hashes one regular non-symlink executable against its pin.
func VerifyArtifact(path, want string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("artifact is not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return errors.New("artifact changed identity before verification")
	}
	hash := sha256.New()
	copied, copyErr := io.Copy(hash, io.LimitReader(file, maxArtifactBytes+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if copied > maxArtifactBytes {
		return fmt.Errorf("artifact exceeds %d bytes", maxArtifactBytes)
	}
	if statErr != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return errors.New("artifact changed while being verified")
	}
	if closeErr != nil {
		return closeErr
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return errors.New("artifact changed identity during verification")
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if got != want {
		return fmt.Errorf("artifact digest is %s, want %s", got, want)
	}
	return nil
}

type InstallOptions struct {
	Source, UpdateName string
	Arguments          []string
	ArgumentsSet       bool
	Permissions        Permissions
	PermissionsSet     bool
	Timeout            time.Duration
	TimeoutSet         bool
}

func (store Store) Install(ctx context.Context, options InstallOptions) (Installation, error) {
	if options.Source == "" {
		return Installation{}, errors.New("adapter source is required")
	}
	absolute, err := filepath.Abs(options.Source)
	if err != nil {
		return Installation{}, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Installation{}, fmt.Errorf("inspect adapter source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Installation{}, errors.New("adapter source must be a regular non-symlink file")
	}
	if options.Timeout < 0 || options.Timeout > time.Hour {
		return Installation{}, errors.New("adapter timeout must be at most one hour")
	}
	if len(options.Arguments) > 63 {
		return Installation{}, errors.New("adapter arguments may contain at most 63 values")
	}
	for index, argument := range options.Arguments {
		if err := validRegistryText(fmt.Sprintf("adapter argument[%d]", index), argument, 32<<10, true); err != nil {
			return Installation{}, err
		}
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(absolute), ".exe") {
		return Installation{}, errors.New("a managed Windows adapter must be an .exe artifact")
	}
	var installed Installation
	err = store.withLock(ctx, func() error {
		current, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		return store.installUnlocked(ctx, current, options, absolute, &installed)
	})
	return installed, err
}

func (store Store) installUnlocked(ctx context.Context, current []Installation, options InstallOptions, source string, result *Installation) error {
	updateIndex := -1
	if options.UpdateName != "" {
		updateIndex = slices.IndexFunc(current, func(value Installation) bool { return value.Name == options.UpdateName })
		if updateIndex < 0 {
			return fmt.Errorf("adapter %q is not installed", options.UpdateName)
		}
		previous := current[updateIndex]
		if !options.ArgumentsSet {
			options.Arguments = append([]string(nil), previous.Arguments...)
		}
		if !options.PermissionsSet {
			options.Permissions = previous.Permissions
		}
		if !options.TimeoutSet {
			options.Timeout, _ = time.ParseDuration(previous.Timeout)
		}
	}
	if err := os.MkdirAll(filepath.Join(store.Root, "bin"), 0o700); err != nil {
		return err
	}
	expected, err := os.Lstat(source)
	if err != nil || expected.Mode()&os.ModeSymlink != 0 || !expected.Mode().IsRegular() {
		return errors.New("adapter source changed before copy")
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	opened, err := sourceFile.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return errors.New("adapter source changed before copy")
	}
	if opened.Size() > maxArtifactBytes {
		return errors.New("adapter source exceeds 512 MiB")
	}
	binDirectory := filepath.Join(store.Root, "bin")
	if info, err := os.Lstat(binDirectory); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("adapter bin path must be a non-symlink directory")
	}
	temporary, err := os.CreateTemp(binDirectory, ".install-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(sourceFile, maxArtifactBytes+1))
	if err != nil {
		temporary.Close()
		return err
	}
	after, statErr := sourceFile.Stat()
	if copied > maxArtifactBytes || statErr != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		temporary.Close()
		return errors.New("adapter source changed while being copied")
	}
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	provisional := filepath.Join(store.Root, "bin", "candidate-"+strings.TrimPrefix(digest, "sha256:")[:16]+ext)
	_ = os.Remove(provisional)
	if err := os.Rename(temporaryPath, provisional); err != nil {
		return fmt.Errorf("publish candidate adapter: %w", err)
	}
	temporaryPath, cleanup = provisional, true
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	capabilities, _, err := store.Runner.Describe(probeCtx, Executable{Path: provisional, Args: options.Arguments})
	if err != nil {
		return err
	}
	if options.UpdateName != "" && capabilities.Provider.Name != options.UpdateName {
		return fmt.Errorf("updated adapter provider is %q, want %q", capabilities.Provider.Name, options.UpdateName)
	}
	capabilityDigest, _ := CapabilityDigest(capabilities)
	name := capabilities.Provider.Name
	if isReservedAdapterName(name) {
		return fmt.Errorf("adapter provider name %q is reserved by an in-process provider", name)
	}
	index := slices.IndexFunc(current, func(value Installation) bool { return value.Name == name })
	if options.UpdateName == "" && index >= 0 {
		return fmt.Errorf("adapter %q is already installed; use adapters update", name)
	}
	if options.UpdateName != "" {
		index = updateIndex
	}
	artifactName := sanitizeArtifactName(name) + "-" + strings.TrimPrefix(digest, "sha256:")[:16] + ext
	finalPath := filepath.Join(store.Root, "bin", artifactName)
	if finalPath != provisional {
		if _, statErr := os.Lstat(finalPath); statErr == nil {
			if err := VerifyArtifact(finalPath, digest); err != nil {
				return fmt.Errorf("existing managed artifact failed integrity: %w", err)
			}
			if err := os.Remove(provisional); err != nil {
				return err
			}
			cleanup = false
		} else if os.IsNotExist(statErr) {
			if err := os.Rename(provisional, finalPath); err != nil {
				return err
			}
		} else {
			return statErr
		}
		temporaryPath = finalPath
	}
	timeout := ""
	if options.Timeout > 0 {
		timeout = options.Timeout.String()
	}
	installation := Installation{Name: name, Artifact: filepath.Join("bin", artifactName), Arguments: append([]string(nil), options.Arguments...), ArtifactDigest: digest, CapabilityDigest: capabilityDigest, Capabilities: capabilities, Permissions: options.Permissions, Timeout: timeout}
	oldArtifact := ""
	if index >= 0 {
		oldArtifact = current[index].Artifact
		current[index] = installation
	} else {
		current = append(current, installation)
	}
	slices.SortFunc(current, func(a, b Installation) int { return strings.Compare(a.Name, b.Name) })
	if err := store.writeUnlocked(current); err != nil {
		return err
	}
	cleanup = false
	if oldArtifact != "" && oldArtifact != installation.Artifact && !installationUsesArtifact(current, oldArtifact) {
		_ = os.Remove(filepath.Join(store.Root, oldArtifact))
	}
	*result = installation
	return nil
}

func sanitizeArtifactName(value string) string {
	var result strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			result.WriteRune(r)
		} else {
			result.WriteByte('-')
		}
	}
	if result.Len() == 0 {
		return "adapter"
	}
	return result.String()
}

func (store Store) Remove(ctx context.Context, name string) error {
	return store.withLock(ctx, func() error {
		current, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(current, func(value Installation) bool { return value.Name == name })
		if index < 0 {
			return fmt.Errorf("adapter %q is not installed", name)
		}
		artifact := current[index].Artifact
		current = slices.Delete(current, index, index+1)
		if err := store.writeUnlocked(current); err != nil {
			return err
		}
		if !installationUsesArtifact(current, artifact) {
			if err := os.Remove(filepath.Join(store.Root, artifact)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}

func installationUsesArtifact(installations []Installation, artifact string) bool {
	return slices.ContainsFunc(installations, func(value Installation) bool { return value.Artifact == artifact })
}

func (store Store) writeUnlocked(installations []Installation) error {
	document := installationManifest{Schema: InstallationSchema, Installations: installations}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxManifestBytes {
		return errors.New("adapter manifest exceeds size limit")
	}
	temporary, err := os.CreateTemp(store.Root, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path, backup := store.manifestPath(), store.manifestPath()+".previous"
	_ = os.Remove(backup)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryName, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
