package contextquery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
)

const maxSourceFileBytes = 16 << 20

const (
	SourceCurrent         = "current"
	SourceUnavailable     = "unavailable"
	SourceMissingDocument = "missing-document"
	SourceMissing         = "missing"
	SourceUnsafePath      = "unsafe-path"
	SourceNotGitVisible   = "not-git-visible"
	SourceNotRegular      = "not-regular"
	SourceTooLarge        = "too-large"
	SourceInvalidUTF8     = "invalid-utf8"
	SourceChanged         = "changed"
	SourceInvalidRange    = "invalid-range"
	SourceBudget          = "budget-exhausted"
)

type SourceExcerpt struct {
	Status    string       `json:"status"`
	Path      string       `json:"path,omitempty"`
	Hash      string       `json:"hash,omitempty"`
	StartLine int          `json:"start_line,omitempty"`
	EndLine   int          `json:"end_line,omitempty"`
	Lines     []SourceLine `json:"lines,omitempty"`
	Detail    string       `json:"detail,omitempty"`
}

type SourceLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type sourceLoader struct {
	contextLines int
	maxBytes     int
	used         int
	truncated    bool
	files        map[string]sourceFile
}

type sourceFile struct {
	status string
	detail string
	hash   string
	lines  []string
}

func newSourceLoader(contextLines, maxBytes int) *sourceLoader {
	return &sourceLoader{contextLines: contextLines, maxBytes: maxBytes, files: map[string]sourceFile{}}
}

func (loader *sourceLoader) excerpt(ctx context.Context, repository Repository, document graph.Document, sourceRange graph.Range) SourceExcerpt {
	result := SourceExcerpt{Path: document.Path}
	file := loader.file(ctx, repository, document)
	return loader.excerptFromFile(result, file, sourceRange)
}

func (loader *sourceLoader) definitionExcerpt(ctx context.Context, repository Repository, document graph.Document, sourceRange graph.Range, kind string) SourceExcerpt {
	file := loader.file(ctx, repository, document)
	expanded, ok := expandedGoDefinition(file, document.Language, sourceRange, kind)
	if !ok {
		return loader.excerptFromFile(SourceExcerpt{Path: document.Path}, file, sourceRange)
	}
	result := loader.excerptFromFile(SourceExcerpt{Path: document.Path}, file, expanded)
	if result.Status == SourceBudget {
		return loader.excerptFromFile(SourceExcerpt{Path: document.Path}, file, sourceRange)
	}
	return result
}

func (loader *sourceLoader) file(ctx context.Context, repository Repository, document graph.Document) sourceFile {
	key := repository.Root + "\x00" + document.Path + "\x00" + document.ContentHash
	file, ok := loader.files[key]
	if !ok {
		file = loadSourceFile(ctx, repository.Root, document)
		loader.files[key] = file
	}
	return file
}

func (loader *sourceLoader) excerptFromFile(result SourceExcerpt, file sourceFile, sourceRange graph.Range) SourceExcerpt {
	result.Status, result.Detail, result.Hash = file.status, file.detail, file.hash
	if file.status != SourceCurrent {
		if file.status == SourceTooLarge {
			loader.truncated = true
		}
		return result
	}
	first := int(sourceRange.Start.Line)
	last := int(sourceRange.End.Line)
	if sourceRange.End.Column == 0 && last > first {
		last--
	}
	if first < 0 || last < first || first >= len(file.lines) {
		result.Status, result.Detail = SourceInvalidRange, "indexed range is outside the current document"
		return result
	}
	if last >= len(file.lines) {
		last = len(file.lines) - 1
	}
	start := max(0, first-loader.contextLines)
	end := min(len(file.lines)-1, last+loader.contextLines)
	wanted := sourceLines(file.lines, start, end)
	cost := linesCost(wanted)
	if loader.used+cost > loader.maxBytes {
		wanted = sourceLines(file.lines, first, last)
		cost = linesCost(wanted)
	}
	if loader.used+cost > loader.maxBytes {
		loader.truncated = true
		result.Status, result.Detail = SourceBudget, "source excerpt exceeds the remaining response byte budget"
		return result
	}
	loader.used += cost
	result.StartLine, result.EndLine, result.Lines = wanted[0].Number, wanted[len(wanted)-1].Number, wanted
	return result
}

func expandedGoDefinition(file sourceFile, language string, sourceRange graph.Range, kind string) (graph.Range, bool) {
	if file.status != SourceCurrent || language != "go" || (kind != "function" && kind != "method") {
		return graph.Range{}, false
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "source.go", strings.Join(file.lines, "\n"), parser.SkipObjectResolution)
	if err != nil {
		return graph.Range{}, false
	}
	targetLine := int(sourceRange.Start.Line) + 1
	var selected *ast.FuncDecl
	ast.Inspect(parsed, func(node ast.Node) bool {
		declaration, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}
		start, end := fileSet.Position(declaration.Pos()), fileSet.Position(declaration.End())
		if start.Line <= targetLine && targetLine <= end.Line {
			selected = declaration
			return false
		}
		return true
	})
	if selected == nil {
		return graph.Range{}, false
	}
	start, end := fileSet.Position(selected.Pos()), fileSet.Position(selected.End())
	return graph.Range{
		Start: graph.Position{Line: int32(start.Line - 1), Column: int32(start.Column - 1), Byte: int64(start.Offset)},
		End:   graph.Position{Line: int32(end.Line - 1), Column: int32(end.Column - 1), Byte: int64(end.Offset)},
	}, true
}

func loadSourceFile(ctx context.Context, root string, document graph.Document) sourceFile {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return sourceFile{status: SourceUnsafePath, detail: "repository root is invalid"}
	}
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return sourceFile{status: SourceUnsafePath, detail: "repository root cannot be resolved through symlinks"}
	}
	nativePath := filepath.FromSlash(document.Path)
	if document.Path == "" || path.IsAbs(document.Path) || filepath.IsAbs(nativePath) || filepath.VolumeName(nativePath) != "" || path.Clean(document.Path) != document.Path || document.Path == "." || document.Path == ".." || strings.HasPrefix(document.Path, "../") || strings.Contains(document.Path, "\\") {
		return sourceFile{status: SourceUnsafePath, detail: "document path is not a canonical repository-relative Git path"}
	}
	name := filepath.Join(cleanRoot, nativePath)
	relative, err := filepath.Rel(cleanRoot, name)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return sourceFile{status: SourceUnsafePath, detail: "document path escapes the repository root"}
	}
	expected, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return sourceFile{status: SourceMissing, detail: "current source file does not exist"}
	}
	if err != nil {
		return sourceFile{status: SourceUnavailable, detail: "inspect current source: " + err.Error()}
	}
	if !expected.Mode().IsRegular() {
		return sourceFile{status: SourceNotRegular, detail: "current source is not a regular file"}
	}
	resolvedName, err := filepath.EvalSymlinks(name)
	if err != nil {
		return sourceFile{status: SourceUnavailable, detail: "resolve current source through symlinks: " + err.Error()}
	}
	if !pathWithinRoot(resolvedRoot, resolvedName) {
		return sourceFile{status: SourceUnsafePath, detail: "current source resolves outside the repository root"}
	}
	visible, err := gitVisible(ctx, cleanRoot, document.Path)
	if err != nil {
		return sourceFile{status: SourceUnavailable, detail: err.Error()}
	}
	if !visible {
		return sourceFile{status: SourceNotGitVisible, detail: "current source is not tracked or visible as a non-ignored Git path"}
	}
	file, err := os.Open(name)
	if err != nil {
		return sourceFile{status: SourceUnavailable, detail: "open current source: " + err.Error()}
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return sourceFile{status: SourceUnavailable, detail: "source path changed identity while opening"}
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSourceFileBytes+1))
	if err != nil {
		return sourceFile{status: SourceUnavailable, detail: "read current source: " + err.Error()}
	}
	if len(content) > maxSourceFileBytes {
		return sourceFile{status: SourceTooLarge, detail: fmt.Sprintf("current source exceeds %d bytes", maxSourceFileBytes)}
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return sourceFile{status: SourceUnavailable, detail: "source changed while reading"}
	}
	if !utf8.Valid(content) {
		return sourceFile{status: SourceInvalidUTF8, detail: "current source is not valid UTF-8"}
	}
	digest := sha256.Sum256(content)
	hash := "sha256:" + hex.EncodeToString(digest[:])
	if document.ContentHash != "" && document.ContentHash != hash {
		return sourceFile{status: SourceChanged, detail: "current source hash differs from the indexed document; refresh and retry", hash: hash}
	}
	return sourceFile{status: SourceCurrent, hash: hash, lines: strings.Split(string(content), "\n")}
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func gitVisible(ctx context.Context, root, name string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", name)
	command.Dir = root
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: 2 << 20}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 64 << 10}
	if err := command.Run(); err != nil {
		return false, fmt.Errorf("check Git-visible source: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	for _, value := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if string(value) == name {
			return true, nil
		}
	}
	return false, nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (writer *limitedWriter) Write(value []byte) (int, error) {
	original := len(value)
	if len(value) > writer.remaining {
		value = value[:writer.remaining]
	}
	if len(value) != 0 {
		if _, err := writer.writer.Write(value); err != nil {
			return 0, err
		}
		writer.remaining -= len(value)
	}
	return original, nil
}

func sourceLines(lines []string, start, end int) []SourceLine {
	result := make([]SourceLine, 0, end-start+1)
	for index := start; index <= end; index++ {
		result = append(result, SourceLine{Number: index + 1, Text: lines[index]})
	}
	return result
}

func linesCost(lines []SourceLine) int {
	total := 0
	for _, line := range lines {
		total += len(line.Text)
	}
	return total
}
