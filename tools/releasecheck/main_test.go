package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifySnapshot(t *testing.T) {
	dist := makeReleaseFixture(t, "")
	if err := verify(dist, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsUnsafeArchivePath(t *testing.T) {
	dist := makeReleaseFixture(t, "../escape")
	err := verify(dist, "", false)
	if err == nil || !strings.Contains(err.Error(), "unsafe archive path") {
		t.Fatalf("got %v, want unsafe archive path", err)
	}
}

func TestVerifyRejectsChecksumMismatch(t *testing.T) {
	dist := makeReleaseFixture(t, "")
	file := filepath.Join(dist, "weave_0.0.0-SNAPSHOT_darwin_amd64.tar.gz")
	input, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString("changed"); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	err = verify(dist, "", false)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v, want checksum mismatch", err)
	}
}

func makeReleaseFixture(t *testing.T, unsafeName string) string {
	t.Helper()
	dist := t.TempDir()
	var catalog []artifact
	var files []string
	for _, id := range []string{"weave", "weave-adapters"} {
		for _, target := range []struct{ os, arch string }{
			{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"},
		} {
			prefix := "weave"
			if id == "weave-adapters" {
				prefix = "weave_adapters"
			}
			extension := ".tar.gz"
			if target.os == "windows" {
				extension = ".zip"
			}
			name := fmt.Sprintf("%s_0.0.0-SNAPSHOT_%s_%s%s", prefix, target.os, target.arch, extension)
			archivePath := filepath.Join(dist, name)
			contents := expectedContents(id, target.os)
			if unsafeName != "" && id == "weave" && target.os == "darwin" && target.arch == "amd64" {
				contents[unsafeName] = false
			}
			writeArchive(t, archivePath, contents)
			catalog = append(catalog, artifact{Name: name, Path: archivePath, Goos: target.os, Goarch: target.arch, Type: "Archive", Extra: map[string]any{"ID": id}})
			files = append(files, archivePath)

			sbomName := name + ".spdx.json"
			sbomPath := filepath.Join(dist, sbomName)
			writeFile(t, sbomPath, `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"fixture","creationInfo":{"created":"2026-01-02T03:04:05Z"},"packages":[{"name":"weave","SPDXID":"SPDXRef-Package-weave"}]}`)
			catalog = append(catalog, artifact{Name: sbomName, Path: sbomPath, Type: "SBOM", Extra: map[string]any{"ID": "archive-spdx"}})
			files = append(files, sbomPath)
		}
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dist, "artifacts.json"), string(encoded))
	writeChecksums(t, filepath.Join(dist, "checksums.txt"), files)
	return dist
}

func writeArchive(t *testing.T, file string, contents map[string]bool) {
	t.Helper()
	if strings.HasSuffix(file, ".zip") {
		output, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(output)
		for name, executable := range contents {
			header := &zip.FileHeader{Name: name, Method: zip.Deflate}
			header.SetModTime(time.Date(2026, 1, 2, 3, 4, 4, 0, time.UTC))
			if executable {
				header.SetMode(0755)
			} else {
				header.SetMode(0644)
			}
			entry, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(entry, name); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	output, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(output)
	writer := tar.NewWriter(gz)
	for name, executable := range contents {
		mode := int64(0644)
		if executable {
			mode = 0755
		}
		body := []byte(name)
		header := &tar.Header{Name: name, Mode: mode, Size: int64(len(body)), ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Uname: "root", Gname: "root", Typeflag: tar.TypeReg}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeChecksums(t *testing.T, file string, files []string) {
	t.Helper()
	var lines []string
	for _, name := range files {
		input, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, input); err != nil {
			t.Fatal(err)
		}
		if err := input.Close(); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, hex.EncodeToString(hash.Sum(nil))+"  "+filepath.Base(name))
	}
	writeFile(t, file, strings.Join(lines, "\n")+"\n")
}

func writeFile(t *testing.T, file, contents string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}
