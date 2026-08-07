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
	Extensions     []string `json:"extensions,omitempty"`
	Filenames      []string `json:"filenames,omitempty"`
	ProjectMarkers []string `json:"project_markers,omitempty"`
}

// Registration is one explicitly trusted automatic native adapter. Command is
// an argv vector, never a shell fragment. ConfigFingerprint binds the source
// registry path and canonical registration into freshness identity.
type Registration struct {
	Name               string
	Command            []string
	Inputs             Inputs
	Claims             Claims
	Permissions        Permissions
	Timeout            time.Duration
	ConfigFingerprint  string
	CapabilityDigest   string
	ArtifactDigest     string
	Source             string
	IntegrityError     string
	PinnedCapabilities *Capabilities
}

type registryFile struct {
	Schema   string              `json:"schema"`
	Adapters []registrationEntry `json:"adapters"`
}

type registrationEntry struct {
	Name             string      `json:"name"`
	Command          []string    `json:"command"`
	Inputs           Inputs      `json:"inputs"`
	Permissions      Permissions `json:"permissions,omitempty"`
	Timeout          string      `json:"timeout,omitempty"`
	Claims           Claims      `json:"claims"`
	CapabilityDigest string      `json:"capability_digest"`
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
			Name             string      `json:"name"`
			Command          []string    `json:"command"`
			Inputs           Inputs      `json:"inputs"`
			Permissions      Permissions `json:"permissions"`
			Timeout          string      `json:"timeout,omitempty"`
			Claims           Claims      `json:"claims"`
			CapabilityDigest string      `json:"capability_digest"`
		}{registration.Name, registration.Command, registration.Inputs, registration.Permissions, timeout, registration.Claims, registration.CapabilityDigest}
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

	inputs := entry.Inputs
	if len(inputs.Extensions)+len(inputs.Filenames)+len(inputs.ProjectMarkers) == 0 {
		inputs = entry.Claims.Inputs
	}
	inputs, err := normalizeInputs(inputs)
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
	claims := entry.Claims
	if len(claims.Inputs.Extensions)+len(claims.Inputs.Filenames)+len(claims.Inputs.ProjectMarkers) == 0 {
		claims.Inputs = inputs
	}
	claims, err = NormalizeClaims(claims)
	if err != nil {
		return Registration{}, fmt.Errorf("claims: %w", err)
	}
	if entry.CapabilityDigest != "" && !validDigest(entry.CapabilityDigest) {
		return Registration{}, errors.New("capability_digest must be sha256 followed by 64 lowercase hexadecimal digits")
	}
	registration := Registration{
		Name: entry.Name, Command: command, Inputs: claims.Inputs, Claims: claims, Permissions: entry.Permissions,
		Timeout: timeout, ConfigFingerprint: fingerprint, CapabilityDigest: entry.CapabilityDigest, Source: "explicit",
	}
	if entry.CapabilityDigest == "" {
		registration.IntegrityError = "explicit registration has no capability_digest pin"
	}
	return registration, nil
}

// NormalizeClaims validates and canonicalizes the authority used for routing.
// Project markers activate a claim but never constitute an owned input by
// themselves.
func NormalizeClaims(value Claims) (Claims, error) {
	inputs, err := normalizeInputs(value.Inputs)
	if err != nil {
		return Claims{}, err
	}
	if len(inputs.Extensions)+len(inputs.Filenames) == 0 {
		return Claims{}, errors.New("claims must own at least one extension or filename")
	}
	value.Inputs = inputs
	value.Evidence = canonicalStrings(value.Evidence)
	if len(value.Evidence) == 0 {
		value.Evidence = []string{"syntactic"}
	}
	if len(value.Evidence) > 16 {
		return Claims{}, errors.New("claims.evidence may contain at most 16 values")
	}
	for _, evidence := range value.Evidence {
		if err := validRegistryText("evidence", evidence, 256, false); err != nil {
			return Claims{}, err
		}
		if !slices.Contains([]string{"ambiguous", "declared", "exact", "generated", "inferred", "syntactic"}, evidence) {
			return Claims{}, fmt.Errorf("unsupported evidence claim %q", evidence)
		}
	}
	return value, nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && strings.ToLower(value) == value
}

func normalizeInputs(inputs Inputs) (Inputs, error) {
	if len(inputs.Extensions)+len(inputs.Filenames)+len(inputs.ProjectMarkers) == 0 {
		return Inputs{}, errors.New("inputs must declare at least one extension, filename, or project marker")
	}
	if len(inputs.Extensions) > 256 || len(inputs.Filenames) > 256 || len(inputs.ProjectMarkers) > 256 {
		return Inputs{}, errors.New("inputs may declare at most 256 values of each kind")
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
	for _, marker := range inputs.ProjectMarkers {
		if err := validRegistryText("input project marker", marker, 256, false); err != nil {
			return Inputs{}, err
		}
		if marker == "." || marker == ".." || filepath.Base(marker) != marker || strings.ContainsAny(marker, `/\`) {
			return Inputs{}, fmt.Errorf("input project marker %q must be a base name", marker)
		}
		result.ProjectMarkers = append(result.ProjectMarkers, strings.ToLower(marker))
	}
	slices.Sort(result.Extensions)
	result.Extensions = slices.Compact(result.Extensions)
	slices.Sort(result.Filenames)
	result.Filenames = slices.Compact(result.Filenames)
	slices.Sort(result.ProjectMarkers)
	result.ProjectMarkers = slices.Compact(result.ProjectMarkers)
	return result, nil
}

// NormalizeCapabilities validates and canonicalizes the public describe
// contract. The returned value is suitable for hashing and persisted routing.
func NormalizeCapabilities(value Capabilities) (Capabilities, error) {
	if err := validRegistryText("provider name", value.Provider.Name, 256, false); err != nil {
		return Capabilities{}, err
	}
	if strings.TrimSpace(value.Provider.Name) != value.Provider.Name || strings.ContainsAny(value.Provider.Name, " \t\r\n/\\") {
		return Capabilities{}, fmt.Errorf("provider name %q must be a single identifier", value.Provider.Name)
	}
	if err := validRegistryText("provider version", value.Provider.Version, 256, false); err != nil {
		return Capabilities{}, err
	}
	if len(value.Claims.Evidence) == 0 {
		return Capabilities{}, errors.New("claims.evidence must be nonempty")
	}
	claims, err := NormalizeClaims(value.Claims)
	if err != nil {
		return Capabilities{}, err
	}
	value.Claims = claims
	for label, values := range map[string][]string{
		"protocol": value.Protocols, "language": value.Languages, "operation": value.Operations,
		"refresh mode": value.RefreshModes, "position encoding": value.PositionEncoding,
		"evidence": value.Claims.Evidence, "required executable": value.Requires.Executables,
	} {
		if len(values) > 256 {
			return Capabilities{}, fmt.Errorf("%s list may contain at most 256 values", label)
		}
		for _, item := range values {
			if err := validRegistryText(label, item, 256, false); err != nil {
				return Capabilities{}, err
			}
		}
	}
	if len(value.Languages) == 0 {
		return Capabilities{}, errors.New("languages must be nonempty")
	}
	value.Protocols = canonicalStrings(value.Protocols)
	value.Languages = canonicalStrings(value.Languages)
	value.Operations = canonicalStrings(value.Operations)
	value.RefreshModes = canonicalStrings(value.RefreshModes)
	value.PositionEncoding = canonicalStrings(value.PositionEncoding)
	value.Requires.Executables = canonicalStrings(value.Requires.Executables)
	for _, language := range value.Languages {
		if language != "*" && language != strings.ToLower(language) {
			return Capabilities{}, fmt.Errorf("language %q must be lowercase", language)
		}
	}
	if !slices.Contains(value.Protocols, Protocol) {
		return Capabilities{}, fmt.Errorf("adapter does not support %s", Protocol)
	}
	if !slices.Contains(value.Operations, "index") || value.FactEncoding != FactEncoding {
		return Capabilities{}, fmt.Errorf("adapter does not support index with %s", FactEncoding)
	}
	if !slices.Contains(value.RefreshModes, "full") {
		return Capabilities{}, errors.New("adapter refresh_modes must include full")
	}
	if !slices.Contains(value.PositionEncoding, "utf8-byte") {
		return Capabilities{}, errors.New("adapter position_encodings must include utf8-byte")
	}
	return value, nil
}

func canonicalStrings(values []string) []string {
	result := append([]string{}, values...)
	slices.Sort(result)
	return slices.Compact(result)
}

// CapabilityDigest binds automatic routing to an installed describe document.
func CapabilityDigest(value Capabilities) (string, error) {
	normalized, err := NormalizeCapabilities(value)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ClaimsDigest binds configured routing authority to the executable's
// negotiated claims without also pinning its release version or requirements.
func ClaimsDigest(value Claims) (string, error) {
	normalized, err := NormalizeClaims(value)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
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
	return slices.Contains([]string{"weave-go", "weave-workspace", "weave-bridges"}, name)
}
