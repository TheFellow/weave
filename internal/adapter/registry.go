package adapter

import (
	"bytes"
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
)

const (
	// RegistryEnvironment selects an explicit, user-trusted adapter registry.
	// Weave never searches for this file inside a repository.
	RegistryEnvironment = "WEAVE_ADAPTER_CONFIG"
	RegistrySchema      = "weave.adapters/v1"
	maxRegistryBytes    = 1 << 20
	maxRegistrations    = 64
)

// Inputs declares the Git-visible files that invalidate an adapter. Extensions
// include their leading dot; filenames are repository-path base names.
type Inputs struct {
	Extensions []string `json:"extensions,omitempty"`
	Filenames  []string `json:"filenames,omitempty"`
}

// Registration is one explicitly trusted automatic native adapter. Command is
// an argv vector, never a shell fragment. ConfigFingerprint binds the source
// registry path and canonical registration into freshness identity.
type Registration struct {
	Name              string
	Command           []string
	Inputs            Inputs
	Permissions       Permissions
	Timeout           time.Duration
	ConfigFingerprint string
}

type registryFile struct {
	Schema   string              `json:"schema"`
	Adapters []registrationEntry `json:"adapters"`
}

type registrationEntry struct {
	Name        string      `json:"name"`
	Command     []string    `json:"command"`
	Inputs      Inputs      `json:"inputs"`
	Permissions Permissions `json:"permissions,omitempty"`
	Timeout     string      `json:"timeout,omitempty"`
}

// LoadRegistry strictly loads an explicitly selected adapter registry. An
// empty path means no third-party registrations; there is intentionally no
// repository-relative or implicit user-directory fallback.
func LoadRegistry(path string) ([]Registration, error) {
	if path == "" {
		return nil, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve adapter registry %q: %w", path, err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open adapter registry %q: %w", absolute, err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxRegistryBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read adapter registry %q: %w", absolute, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close adapter registry %q: %w", absolute, closeErr)
	}
	if len(content) > maxRegistryBytes {
		return nil, fmt.Errorf("adapter registry %q exceeds %d bytes", absolute, maxRegistryBytes)
	}

	var document registryFile
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode adapter registry %q: %w", absolute, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode adapter registry %q: %w", absolute, err)
	}
	if document.Schema != RegistrySchema {
		return nil, fmt.Errorf("adapter registry %q has schema %q, want %q", absolute, document.Schema, RegistrySchema)
	}
	if len(document.Adapters) > maxRegistrations {
		return nil, fmt.Errorf("adapter registry %q has %d registrations, maximum is %d", absolute, len(document.Adapters), maxRegistrations)
	}

	registrations := make([]Registration, 0, len(document.Adapters))
	seen := map[string]bool{}
	for index, entry := range document.Adapters {
		registration, err := validateRegistration(entry, filepath.Dir(absolute), "")
		if err != nil {
			return nil, fmt.Errorf("adapter registry %q registration %d: %w", absolute, index, err)
		}
		timeout := ""
		if registration.Timeout != 0 {
			timeout = registration.Timeout.String()
		}
		canonical := struct {
			Name        string      `json:"name"`
			Command     []string    `json:"command"`
			Inputs      Inputs      `json:"inputs"`
			Permissions Permissions `json:"permissions"`
			Timeout     string      `json:"timeout,omitempty"`
		}{registration.Name, registration.Command, registration.Inputs, registration.Permissions, timeout}
		encodedEntry, _ := json.Marshal(canonical)
		entryHash := sha256.Sum256(append(append([]byte(filepath.Clean(absolute)), 0), encodedEntry...))
		registration.ConfigFingerprint = "sha256:" + hex.EncodeToString(entryHash[:])
		if isReservedAdapterName(registration.Name) {
			return nil, fmt.Errorf("adapter registry %q registration %d: name %q is reserved by a built-in tool", absolute, index, registration.Name)
		}
		if seen[registration.Name] {
			return nil, fmt.Errorf("adapter registry %q has duplicate name %q", absolute, registration.Name)
		}
		seen[registration.Name] = true
		registrations = append(registrations, registration)
	}
	slices.SortFunc(registrations, func(a, b Registration) int { return strings.Compare(a.Name, b.Name) })
	return registrations, nil
}

func validateRegistration(entry registrationEntry, registryDirectory, fingerprint string) (Registration, error) {
	if err := validRegistryText("name", entry.Name, 256, false); err != nil {
		return Registration{}, err
	}
	if strings.TrimSpace(entry.Name) != entry.Name || strings.ContainsAny(entry.Name, " \t\r\n/\\") {
		return Registration{}, fmt.Errorf("name %q must be a single provider identifier", entry.Name)
	}
	if len(entry.Command) == 0 || len(entry.Command) > 64 {
		return Registration{}, errors.New("command must contain between 1 and 64 argv values")
	}
	command := append([]string(nil), entry.Command...)
	for index, value := range command {
		if err := validRegistryText(fmt.Sprintf("command[%d]", index), value, 32<<10, index != 0); err != nil {
			return Registration{}, err
		}
	}
	if strings.ContainsAny(command[0], `/\`) && !filepath.IsAbs(command[0]) {
		command[0] = filepath.Clean(filepath.Join(registryDirectory, command[0]))
	}

	inputs, err := normalizeInputs(entry.Inputs)
	if err != nil {
		return Registration{}, err
	}
	timeout := time.Duration(0)
	if entry.Timeout != "" {
		timeout, err = time.ParseDuration(entry.Timeout)
		if err != nil || timeout <= 0 || timeout > time.Hour {
			return Registration{}, fmt.Errorf("timeout %q must be a duration greater than zero and at most one hour", entry.Timeout)
		}
	}
	return Registration{
		Name: entry.Name, Command: command, Inputs: inputs, Permissions: entry.Permissions,
		Timeout: timeout, ConfigFingerprint: fingerprint,
	}, nil
}

func normalizeInputs(inputs Inputs) (Inputs, error) {
	if len(inputs.Extensions)+len(inputs.Filenames) == 0 {
		return Inputs{}, errors.New("inputs must declare at least one extension or filename")
	}
	if len(inputs.Extensions) > 256 || len(inputs.Filenames) > 256 {
		return Inputs{}, errors.New("inputs may declare at most 256 extensions and 256 filenames")
	}
	result := Inputs{}
	for _, extension := range inputs.Extensions {
		if err := validRegistryText("input extension", extension, 256, false); err != nil {
			return Inputs{}, err
		}
		if !strings.HasPrefix(extension, ".") || len(extension) == 1 || strings.ContainsAny(extension, `/\\`) {
			return Inputs{}, fmt.Errorf("input extension %q must begin with a dot and contain no path separator", extension)
		}
		result.Extensions = append(result.Extensions, strings.ToLower(extension))
	}
	for _, filename := range inputs.Filenames {
		if err := validRegistryText("input filename", filename, 256, false); err != nil {
			return Inputs{}, err
		}
		if filename == "." || filename == ".." || filepath.Base(filename) != filename || strings.ContainsAny(filename, `/\\`) {
			return Inputs{}, fmt.Errorf("input filename %q must be a base name", filename)
		}
		result.Filenames = append(result.Filenames, strings.ToLower(filename))
	}
	slices.Sort(result.Extensions)
	result.Extensions = slices.Compact(result.Extensions)
	slices.Sort(result.Filenames)
	result.Filenames = slices.Compact(result.Filenames)
	return result, nil
}

func validRegistryText(label, value string, maximum int, allowEmpty bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s is not valid NUL-free UTF-8", label)
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values are not allowed")
}

func isReservedAdapterName(name string) bool {
	return slices.Contains([]string{
		"dotnet", "scip-dotnet", "scip:scip-clang", "scip:scip-typescript",
		"weave-cpp", "weave-dotnet", "weave-jvm", "weave-python", "weave-rust", "weave-typescript",
	}, name)
}
