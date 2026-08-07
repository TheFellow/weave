// Command releasecheck validates the release artifacts produced by GoReleaser.
// It is a repository tool, not part of the installed Weave CLI.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type artifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Goos   string         `json:"goos"`
	Goarch string         `json:"goarch"`
	Type   string         `json:"type"`
	Extra  map[string]any `json:"extra"`
}

type archiveEntry struct {
	name    string
	mode    fs.FileMode
	mtime   time.Time
	regular bool
	dir     bool
	owner   string
	group   string
}

func main() {
	dist := flag.String("dist", "dist", "GoReleaser output directory")
	extra := flag.String("extra", "", "optional companion artifact directory")
	requireCompanions := flag.Bool("require-companions", false, "require .NET and Python companion packages")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/releasecheck [--dist DIR] [--extra DIR] [--require-companions]")
		os.Exit(2)
	}
	if err := verify(*dist, *extra, *requireCompanions); err != nil {
		fmt.Fprintf(os.Stderr, "releasecheck: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("release artifacts verified")
}

func verify(dist, extra string, requireCompanions bool) error {
	dist, err := filepath.Abs(dist)
	if err != nil {
		return err
	}
	var catalog []artifact
	encoded, err := os.ReadFile(filepath.Join(dist, "artifacts.json"))
	if err != nil {
		return fmt.Errorf("read artifacts.json: %w", err)
	}
	if err := json.Unmarshal(encoded, &catalog); err != nil {
		return fmt.Errorf("decode artifacts.json: %w", err)
	}

	archives := make(map[string]artifact)
	sboms := make(map[string]artifact)
	targets := make(map[string]map[string]bool)
	for _, item := range catalog {
		switch item.Type {
		case "Archive":
			id, _ := item.Extra["ID"].(string)
			if id != "weave" && id != "weave-adapters" {
				return fmt.Errorf("archive %q has unexpected archive id %q", item.Name, id)
			}
			if item.Goos == "" || item.Goarch == "" {
				return fmt.Errorf("archive %q has no target", item.Name)
			}
			if _, duplicate := archives[item.Name]; duplicate {
				return fmt.Errorf("duplicate archive %q", item.Name)
			}
			archives[item.Name] = item
			if targets[id] == nil {
				targets[id] = make(map[string]bool)
			}
			key := item.Goos + "/" + item.Goarch
			if targets[id][key] {
				return fmt.Errorf("archive id %q has duplicate target %s", id, key)
			}
			targets[id][key] = true
		case "SBOM":
			if _, duplicate := sboms[item.Name]; duplicate {
				return fmt.Errorf("duplicate SBOM %q", item.Name)
			}
			sboms[item.Name] = item
		}
	}
	if err := validateTargets(targets); err != nil {
		return err
	}
	if len(sboms) != len(archives) {
		return fmt.Errorf("got %d SBOMs for %d archives", len(sboms), len(archives))
	}

	archiveNames := sortedArtifactNames(archives)
	for _, name := range archiveNames {
		item := archives[name]
		file, err := catalogPath(dist, item)
		if err != nil {
			return err
		}
		entries, err := readArchive(file)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		id, _ := item.Extra["ID"].(string)
		if err := validateArchive(entries, expectedContents(id, item.Goos)); err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		sbomName := name + ".spdx.json"
		sbom, ok := sboms[sbomName]
		if !ok {
			return fmt.Errorf("archive %q has no matching %q", name, sbomName)
		}
		sbomPath, err := catalogPath(dist, sbom)
		if err != nil {
			return err
		}
		if err := validateSPDX(sbomPath); err != nil {
			return fmt.Errorf("validate %s: %w", sbomName, err)
		}
	}

	roots := []string{dist}
	if extra != "" {
		resolved, err := filepath.Abs(extra)
		if err != nil {
			return err
		}
		roots = append(roots, resolved)
	}
	checksums, err := verifyChecksums(filepath.Join(dist, "checksums.txt"), roots)
	if err != nil {
		return err
	}
	for _, name := range archiveNames {
		if !checksums[name] || !checksums[name+".spdx.json"] {
			return fmt.Errorf("checksums.txt does not cover archive and SBOM %q", name)
		}
	}
	if extra != "" {
		if err := verifyExtraCoverage(extra, checksums, requireCompanions); err != nil {
			return err
		}
	} else if requireCompanions {
		return errors.New("--require-companions requires --extra")
	}
	return nil
}

func validateTargets(got map[string]map[string]bool) error {
	want := []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}
	for _, id := range []string{"weave", "weave-adapters"} {
		if len(got[id]) != len(want) {
			return fmt.Errorf("archive id %q has %d targets, want %d", id, len(got[id]), len(want))
		}
		for _, target := range want {
			if !got[id][target] {
				return fmt.Errorf("archive id %q is missing %s", id, target)
			}
		}
	}
	return nil
}

func sortedArtifactNames(values map[string]artifact) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func catalogPath(dist string, item artifact) (string, error) {
	if item.Path == "" {
		return "", fmt.Errorf("artifact %q has no path", item.Name)
	}
	file := item.Path
	if !filepath.IsAbs(file) {
		// GoReleaser records paths relative to the repository root (usually
		// dist/name) or to dist depending on the command version.
		if filepath.Base(filepath.Clean(file)) == filepath.Clean(file) {
			file = filepath.Join(dist, file)
		} else {
			file = filepath.Join(filepath.Dir(dist), file)
		}
	}
	file, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dist, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact %q escapes dist: %q", item.Name, item.Path)
	}
	if filepath.Base(file) != item.Name {
		return "", fmt.Errorf("artifact %q path has different basename %q", item.Name, item.Path)
	}
	return file, nil
}

func readArchive(file string) ([]archiveEntry, error) {
	if strings.HasSuffix(file, ".zip") {
		return readZip(file)
	}
	if strings.HasSuffix(file, ".tar.gz") {
		return readTarGz(file)
	}
	return nil, fmt.Errorf("unsupported archive extension")
}

func readZip(file string) ([]archiveEntry, error) {
	reader, err := zip.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := make([]archiveEntry, 0, len(reader.File))
	for _, item := range reader.File {
		entries = append(entries, archiveEntry{name: item.Name, mode: item.Mode(), mtime: item.Modified.UTC(), regular: item.Mode().IsRegular(), dir: item.FileInfo().IsDir()})
	}
	return entries, nil
}

func readTarGz(file string) ([]archiveEntry, error) {
	input, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var entries []archiveEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		mode := header.FileInfo().Mode()
		entries = append(entries, archiveEntry{name: header.Name, mode: mode, mtime: header.ModTime.UTC(), regular: mode.IsRegular(), dir: mode.IsDir(), owner: header.Uname, group: header.Gname})
	}
	return entries, nil
}

func expectedContents(id, goos string) map[string]bool {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	want := map[string]bool{"README.md": false, "LICENSE": false}
	if id == "weave" {
		want["weave"+ext] = true
		return want
	}
	for _, binary := range []string{"weave-cpp", "weave-typescript", "weave-jvm", "weave-ctags"} {
		want[binary+ext] = true
	}
	for _, file := range []string{"adapters/cpp/README.md", "adapters/typescript/README.md", "adapters/jvm/README.md", "adapters/ctags/README.md"} {
		want[file] = false
	}
	return want
}

func validateArchive(entries []archiveEntry, want map[string]bool) error {
	seen := make(map[string]bool)
	var mtime time.Time
	for _, entry := range entries {
		if err := safeArchivePath(entry.name); err != nil {
			return err
		}
		if entry.dir {
			continue
		}
		if !entry.regular {
			return fmt.Errorf("%q is not a regular file", entry.name)
		}
		if seen[entry.name] {
			return fmt.Errorf("duplicate entry %q", entry.name)
		}
		seen[entry.name] = true
		executable, ok := want[entry.name]
		if !ok {
			return fmt.Errorf("unexpected entry %q", entry.name)
		}
		if executable != (entry.mode.Perm()&0111 != 0) {
			return fmt.Errorf("entry %q has mode %04o, executable=%v", entry.name, entry.mode.Perm(), executable)
		}
		if entry.mtime.IsZero() {
			return fmt.Errorf("entry %q has zero modification time", entry.name)
		}
		if mtime.IsZero() {
			mtime = entry.mtime
		} else if !entry.mtime.Equal(mtime) {
			return fmt.Errorf("entry %q modification time %s differs from %s", entry.name, entry.mtime, mtime)
		}
		if entry.owner != "" && (entry.owner != "root" || entry.group != "root") {
			return fmt.Errorf("entry %q has owner/group %q/%q", entry.name, entry.owner, entry.group)
		}
	}
	for name := range want {
		if !seen[name] {
			return fmt.Errorf("missing entry %q", name)
		}
	}
	return nil
}

func safeArchivePath(name string) error {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	return nil
}

func validateSPDX(file string) error {
	encoded, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var document struct {
		SPDXVersion  string `json:"spdxVersion"`
		SPDXID       string `json:"SPDXID"`
		Name         string `json:"name"`
		CreationInfo struct {
			Created string `json:"created"`
		} `json:"creationInfo"`
		Packages []json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		return err
	}
	if !strings.HasPrefix(document.SPDXVersion, "SPDX-") || document.SPDXID != "SPDXRef-DOCUMENT" || document.Name == "" || document.CreationInfo.Created == "" {
		return errors.New("missing required SPDX document metadata")
	}
	if len(document.Packages) == 0 {
		return errors.New("SBOM contains no packages")
	}
	return nil
}

func verifyChecksums(file string, roots []string) (map[string]bool, error) {
	encoded, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read checksums.txt: %w", err)
	}
	covered := make(map[string]bool)
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(encoded)), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("checksums.txt line %d is not SHA-256 checksum format", lineNumber+1)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("checksums.txt line %d: %w", lineNumber+1, err)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) != name || name == "." {
			return nil, fmt.Errorf("checksums.txt line %d has unsafe filename %q", lineNumber+1, name)
		}
		if covered[name] {
			return nil, fmt.Errorf("checksums.txt repeats %q", name)
		}
		var matches []string
		for _, root := range roots {
			candidate := filepath.Join(root, name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("checksum subject %q resolves to %d files", name, len(matches))
		}
		actual, err := fileSHA256(matches[0])
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(fields[0], actual) {
			return nil, fmt.Errorf("checksum mismatch for %q", name)
		}
		covered[name] = true
	}
	if len(covered) == 0 {
		return nil, errors.New("checksums.txt is empty")
	}
	return covered, nil
}

func fileSHA256(file string) (string, error) {
	input, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyExtraCoverage(directory string, checksums map[string]bool, requireCompanions bool) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read companion artifacts: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
			if !checksums[entry.Name()] {
				return fmt.Errorf("companion artifact %q is not checksummed", entry.Name())
			}
		}
	}
	if !requireCompanions {
		return nil
	}
	patterns := []string{"*.nupkg", "*.whl", "weave-dotnet_*_linux-x64.tar.gz", "weave-dotnet_*_linux-arm64.tar.gz", "weave-dotnet_*_osx-x64.tar.gz", "weave-dotnet_*_osx-arm64.tar.gz", "weave-dotnet_*_win-x64.zip", "weave-dotnet_*_win-arm64.zip"}
	for _, pattern := range patterns {
		found := false
		for _, name := range names {
			if matched, _ := path.Match(pattern, name); matched {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("companion artifacts are missing %q", pattern)
		}
	}
	return nil
}
