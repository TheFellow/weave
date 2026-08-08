package workspaceindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/relationship"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"go.yaml.in/yaml/v3"
	"golang.org/x/net/html"
)

const (
	maxFrontMatterBytes = 1 << 20
	maxMetadataNodes    = 100_000
	maxDestinationBytes = 64 << 10
	maxExtractedText    = 64 << 10
	maxASTNodes         = 250_000
	maxASTDepth         = 1_024
)

var relativeURLExpression = regexp.MustCompile(`^\{\{\s*(['"])(/[^'"]*)(['"])\s*\|\s*relative_url\s*\}\}$`)

type metadata struct {
	Title        string
	Permalink    string
	RedirectFrom []string
	Series       string
	Topics       []string
	Tags         []string
	Categories   []string
}

type documentModel struct {
	repository    string
	path          string
	content       []byte
	metadata      metadata
	headings      []headingModel
	blocks        []blockModel
	links         []linkModel
	generatedFrom string
	lineStarts    []int
}

type headingModel struct {
	title, anchor, id, parent string
	level                     int
	start, end                int
	addressable               bool
}

type blockModel struct {
	language, id, parent string
	start, end           int
	ordinal              int
}

type linkModel struct {
	destination string
	embed       bool
	start, end  int
}

type rawHeading struct {
	title, anchor string
	level         int
	start, end    int
	addressable   bool
}

type byteRange struct{ start, end int }
type generatedMarker struct {
	source string
	start  int
}

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

func parseDocument(repository, name string, content []byte) (*documentModel, error) {
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("document is not valid UTF-8")
	}
	meta, body, err := parseFrontMatter(content)
	if err != nil {
		return nil, err
	}
	root := markdown.Parser().Parse(text.NewReader(body))
	model := &documentModel{
		repository: repository, path: name, content: content, metadata: meta,
		lineStarts: lineStarts(content),
	}
	var headings []rawHeading
	var rawRegions []byteRange
	err = walkAST(root, func(node ast.Node) error {
		switch value := node.(type) {
		case *ast.Heading:
			start, end := nodeExtent(value, body)
			headings = append(headings, rawHeading{title: strings.TrimSpace(string(value.Text(body))), level: value.Level, start: start, end: end, addressable: true})
		case *ast.FencedCodeBlock:
			start, end := nodeExtent(value, body)
			model.blocks = append(model.blocks, blockModel{language: strings.TrimSpace(string(value.Language(body))), start: start, end: end})
		case *ast.Link:
			start, end := destinationExtent(value, body, string(value.Destination))
			model.links = append(model.links, linkModel{destination: string(value.Destination), start: start, end: end})
		case *ast.Image:
			start, end := destinationExtent(value, body, string(value.Destination))
			model.links = append(model.links, linkModel{destination: string(value.Destination), embed: true, start: start, end: end})
		case *ast.AutoLink:
			start, end := nodeExtent(value, body)
			model.links = append(model.links, linkModel{destination: string(value.URL(body)), start: start, end: end})
		case *ast.RawHTML:
			if start, end, ok := segmentExtent(value.Segments); ok {
				rawRegions = append(rawRegions, byteRange{start, end})
			}
		case *ast.HTMLBlock:
			start, end := nodeExtent(value, body)
			if value.HasClosure() {
				end = max(end, value.ClosureLine.Stop)
			}
			rawRegions = append(rawRegions, byteRange{start, end})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var generated []string
	for _, region := range mergeRanges(rawRegions) {
		rawHeadings, rawLinks, rawGenerated, err := parseHTML(body[region.start:region.end], region.start)
		if err != nil {
			return nil, err
		}
		headings = append(headings, rawHeadings...)
		model.links = append(model.links, rawLinks...)
		for _, marker := range rawGenerated {
			if strings.TrimSpace(string(body[:marker.start])) == "" {
				generated = append(generated, marker.source)
			}
		}
	}
	for _, link := range model.links {
		if len(link.destination) > maxDestinationBytes {
			return nil, fmt.Errorf("link destination exceeds %d bytes", maxDestinationBytes)
		}
	}
	model.finishHeadings(headings)
	model.finishBlocks()
	slices.SortFunc(model.links, func(a, b linkModel) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return strings.Compare(a.destination, b.destination)
	})
	model.links = slices.CompactFunc(model.links, func(a, b linkModel) bool {
		return a.start == b.start && a.end == b.end && a.embed == b.embed && a.destination == b.destination
	})
	slices.Sort(generated)
	generated = slices.Compact(generated)
	// Conflicting provenance is not unique enough to assert. Preserve all other
	// document semantics and simply omit a generates edge.
	if len(generated) == 1 {
		model.generatedFrom = generated[0]
	}
	if err := model.validateExtractedText(); err != nil {
		return nil, err
	}
	return model, nil
}

func (model *documentModel) validateExtractedText() error {
	values := []string{model.metadata.Title, model.metadata.Permalink, model.metadata.Series, model.generatedFrom}
	values = append(values, model.metadata.RedirectFrom...)
	values = append(values, model.metadata.Topics...)
	values = append(values, model.metadata.Tags...)
	values = append(values, model.metadata.Categories...)
	for _, heading := range model.headings {
		values = append(values, heading.title, heading.anchor)
	}
	for _, block := range model.blocks {
		values = append(values, block.language)
	}
	for _, value := range values {
		if len(value) > maxExtractedText {
			return fmt.Errorf("extracted text exceeds %d bytes", maxExtractedText)
		}
	}
	return nil
}

func (model *documentModel) factUpperBound() int {
	metadataEdges := len(model.metadata.RedirectFrom) + len(model.metadata.Topics) + len(model.metadata.Tags) + len(model.metadata.Categories)
	if model.metadata.Permalink != "" {
		metadataEdges++
	}
	if model.metadata.Series != "" {
		metadataEdges++
	}
	if model.generatedFrom != "" {
		metadataEdges++
	}
	// document + root/heading/block symbols + root/heading/link occurrences +
	// heading/block/link/metadata edges. This intentionally overestimates.
	return 1 + (1 + len(model.headings) + len(model.blocks)) + (1 + len(model.headings) + len(model.links)) +
		(len(model.headings) + len(model.blocks) + len(model.links) + metadataEdges)
}

func walkAST(root ast.Node, visit func(ast.Node) error) error {
	type pending struct {
		node  ast.Node
		depth int
	}
	stack := []pending{{node: root}}
	count := 0
	for len(stack) != 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		count++
		if count > maxASTNodes {
			return fmt.Errorf("Markdown AST exceeds %d nodes", maxASTNodes)
		}
		if current.depth > maxASTDepth {
			return fmt.Errorf("Markdown AST exceeds depth %d", maxASTDepth)
		}
		if err := visit(current.node); err != nil {
			return err
		}
		for child := current.node.LastChild(); child != nil; child = child.PreviousSibling() {
			stack = append(stack, pending{node: child, depth: current.depth + 1})
		}
	}
	return nil
}

func parseFrontMatter(content []byte) (metadata, []byte, error) {
	body := append([]byte(nil), content...)
	if !bytes.HasPrefix(content, []byte("---\n")) && !bytes.HasPrefix(content, []byte("---\r\n")) {
		return metadata{}, body, nil
	}
	opening := bytes.IndexByte(content, '\n') + 1
	closingStart, closingEnd := -1, -1
	for cursor := opening; cursor <= len(content); {
		next := bytes.IndexByte(content[cursor:], '\n')
		end := len(content)
		if next >= 0 {
			end = cursor + next
		}
		line := bytes.TrimSuffix(content[cursor:end], []byte{'\r'})
		if bytes.Equal(line, []byte("---")) {
			closingStart, closingEnd = cursor, end
			if closingEnd < len(content) {
				closingEnd++
			}
			break
		}
		if next < 0 {
			break
		}
		cursor = end + 1
	}
	if closingStart < 0 {
		return metadata{}, nil, fmt.Errorf("unterminated YAML front matter")
	}
	if closingStart-opening > maxFrontMatterBytes {
		return metadata{}, nil, fmt.Errorf("YAML front matter exceeds %d bytes", maxFrontMatterBytes)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(content[opening:closingStart], &root); err != nil {
		return metadata{}, nil, fmt.Errorf("decode YAML front matter: %w", err)
	}
	if countYAMLNodes(&root, maxMetadataNodes+1) > maxMetadataNodes {
		return metadata{}, nil, fmt.Errorf("YAML front matter exceeds %d nodes", maxMetadataNodes)
	}
	meta := metadata{}
	if len(root.Content) != 0 {
		mapping := root.Content[0]
		for index := 0; mapping.Kind == yaml.MappingNode && index+1 < len(mapping.Content); index += 2 {
			key, value := mapping.Content[index].Value, mapping.Content[index+1]
			switch key {
			case "title":
				meta.Title = scalar(value)
			case "permalink":
				meta.Permalink = scalar(value)
			case "redirect_from":
				meta.RedirectFrom = stringValues(value)
			case "series":
				meta.Series = scalar(value)
			case "topics":
				meta.Topics = stringValues(value)
			case "tags":
				meta.Tags = stringValues(value)
			case "categories":
				meta.Categories = stringValues(value)
			}
		}
	}
	for index := 0; index < closingEnd; index++ {
		if body[index] != '\n' && body[index] != '\r' {
			body[index] = ' '
		}
	}
	return meta, body, nil
}

func countYAMLNodes(node *yaml.Node, limit int) int {
	if node == nil || limit <= 0 {
		return 0
	}
	stack := []*yaml.Node{node}
	count := 0
	for len(stack) != 0 && count < limit {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		count++
		for _, child := range current.Content {
			stack = append(stack, child)
		}
	}
	return count
}

func scalar(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode {
		return strings.TrimSpace(node.Value)
	}
	return ""
}

func stringValues(node *yaml.Node) []string {
	if node.Kind == yaml.ScalarNode {
		if value := strings.TrimSpace(node.Value); value != "" {
			return []string{value}
		}
		return nil
	}
	var result []string
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if value := scalar(child); value != "" {
				result = append(result, value)
			}
		}
	}
	return result
}

func (model *documentModel) finishHeadings(values []rawHeading) {
	slices.SortFunc(values, func(a, b rawHeading) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.level - b.level
	})
	counts := map[string]int{}
	stack := make([]headingModel, 0, 6)
	for _, value := range values {
		if value.title == "" {
			continue
		}
		base := value.anchor
		if base == "" {
			base = headingAnchor(value.title)
		}
		ordinal := counts[base]
		counts[base]++
		anchor := base
		if ordinal != 0 {
			anchor = fmt.Sprintf("%s-%d", base, ordinal)
		}
		for len(stack) != 0 && stack[len(stack)-1].level >= value.level {
			stack = stack[:len(stack)-1]
		}
		parent := fileSymbolID(model.repository, model.path)
		if len(stack) != 0 {
			parent = stack[len(stack)-1].id
		}
		heading := headingModel{
			title: value.title, anchor: anchor, level: value.level, start: value.start, end: value.end,
			id: sectionSymbolID(model.repository, model.path, anchor), parent: parent, addressable: value.addressable,
		}
		model.headings = append(model.headings, heading)
		stack = append(stack, heading)
	}
}

func (model *documentModel) finishBlocks() {
	slices.SortFunc(model.blocks, func(a, b blockModel) int { return a.start - b.start })
	for index := range model.blocks {
		block := &model.blocks[index]
		block.ordinal = index + 1
		block.parent = model.sectionAt(block.start)
		block.id = stableID("code-block", model.repository, model.path, fmt.Sprintf("%d", block.ordinal))
	}
}

func (model *documentModel) sectionAt(offset int) string {
	result := fileSymbolID(model.repository, model.path)
	for _, heading := range model.headings {
		if heading.start > offset {
			break
		}
		result = heading.id
	}
	return result
}

func (model *documentModel) headingSearchTerms(index int) []string {
	heading := model.headings[index]
	end := len(model.content)
	if index+1 < len(model.headings) {
		end = model.headings[index+1].start
	}
	start := min(max(heading.end, 0), end)
	return graph.ExtractSearchTerms(string(model.content[start:end]))
}

func (model *documentModel) preludeSearchTerms() []string {
	end := len(model.content)
	if len(model.headings) != 0 {
		end = model.headings[0].start
	}
	metadata := append([]string{model.metadata.Title, model.metadata.Series}, model.metadata.Topics...)
	metadata = append(metadata, model.metadata.Tags...)
	metadata = append(metadata, model.metadata.Categories...)
	return graph.ExtractSearchTerms(strings.Join(metadata, " ") + " " + string(model.content[:max(0, end)]))
}

func parseHTML(content []byte, base int) ([]rawHeading, []linkModel, []generatedMarker, error) {
	tokenizer := html.NewTokenizer(bytes.NewReader(content))
	tokenizer.SetMaxBuf(maxDestinationBytes * 2)
	var headings []rawHeading
	var links []linkModel
	var generated []generatedMarker
	cursor := 0
	type openHeading struct {
		level, start int
		anchor       string
		addressable  bool
		text         strings.Builder
	}
	var current *openHeading
	for {
		tokenType := tokenizer.Next()
		raw := tokenizer.Raw()
		start := base + cursor
		end := start + len(raw)
		cursor += len(raw)
		if tokenType == html.ErrorToken {
			if err := tokenizer.Err(); err != nil && err != io.EOF {
				return nil, nil, nil, fmt.Errorf("tokenize inert HTML: %w", err)
			}
			break
		}
		token := tokenizer.Token()
		switch tokenType {
		case html.StartTagToken, html.SelfClosingTagToken:
			if len(token.Data) == 2 && token.Data[0] == 'h' && token.Data[1] >= '1' && token.Data[1] <= '6' {
				current = &openHeading{level: int(token.Data[1] - '0'), start: start}
				for _, attribute := range token.Attr {
					if (attribute.Key == "id" || attribute.Key == "name") && attribute.Val != "" {
						current.anchor, current.addressable = attribute.Val, true
						break
					}
				}
			}
			for _, attribute := range token.Attr {
				key := strings.ToLower(attribute.Key)
				if key != "href" && key != "src" {
					continue
				}
				attributeStart, attributeEnd := start, end
				if relative := bytes.Index(raw, []byte(attribute.Val)); relative >= 0 {
					attributeStart, attributeEnd = start+relative, start+relative+len(attribute.Val)
				}
				links = append(links, linkModel{destination: attribute.Val, embed: key == "src", start: attributeStart, end: attributeEnd})
			}
		case html.TextToken:
			if current != nil {
				current.text.WriteString(token.Data)
			}
		case html.EndTagToken:
			if current != nil && token.Data == fmt.Sprintf("h%d", current.level) {
				title := strings.TrimSpace(current.text.String())
				anchor := current.anchor
				if anchor == "" {
					anchor = "html:" + headingAnchor(title)
				}
				headings = append(headings, rawHeading{title: title, anchor: anchor, addressable: current.addressable, level: current.level, start: current.start, end: end})
				current = nil
			}
		case html.CommentToken:
			if source := generatedSource([]byte("<!--" + token.Data + "-->")); source != "" {
				generated = append(generated, generatedMarker{source: source, start: start})
			}
		}
	}
	return headings, links, generated, nil
}

func nodeExtent(node ast.Node, source []byte) (int, int) {
	start, end := len(source), 0
	if position := node.Pos(); position >= 0 {
		start, end = min(start, position), max(end, position+1)
	}
	if node.Type() == ast.TypeBlock {
		lines := node.Lines()
		for index := 0; index < lines.Len(); index++ {
			segment := lines.At(index)
			start, end = min(start, segment.Start), max(end, segment.Stop)
		}
	}
	_ = walkAST(node, func(child ast.Node) error {
		if value, ok := child.(*ast.Text); ok {
			start, end = min(start, value.Segment.Start), max(end, value.Segment.Stop)
		}
		return nil
	})
	if start > end {
		return 0, 0
	}
	return start, end
}

func segmentExtent(segments *text.Segments) (int, int, bool) {
	if segments == nil || segments.Len() == 0 {
		return 0, 0, false
	}
	start, end := segments.At(0).Start, segments.At(0).Stop
	for index := 1; index < segments.Len(); index++ {
		segment := segments.At(index)
		start, end = min(start, segment.Start), max(end, segment.Stop)
	}
	return start, end, true
}

func destinationExtent(node ast.Node, source []byte, destination string) (int, int) {
	start, end := nodeExtent(node, source)
	lineStart := bytes.LastIndexByte(source[:min(start, len(source))], '\n') + 1
	lineEnd := len(source)
	if next := bytes.IndexByte(source[min(end, len(source)):], '\n'); next >= 0 {
		lineEnd = min(end, len(source)) + next
	}
	if destination != "" {
		searchStart := max(lineStart, min(start, lineEnd))
		if relative := bytes.Index(source[searchStart:lineEnd], []byte(destination)); relative >= 0 {
			return searchStart + relative, searchStart + relative + len(destination)
		}
	}
	return start, end
}

func mergeRanges(values []byteRange) []byteRange {
	slices.SortFunc(values, func(a, b byteRange) int { return a.start - b.start })
	var result []byteRange
	for _, value := range values {
		if value.end <= value.start {
			continue
		}
		if len(result) != 0 && value.start <= result[len(result)-1].end {
			result[len(result)-1].end = max(result[len(result)-1].end, value.end)
			continue
		}
		result = append(result, value)
	}
	return result
}

func headingAnchor(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\<`, "<"))
	value = strings.ReplaceAll(value, `\>`, ">")
	var result strings.Builder
	previousHyphen := false
	for _, current := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(current) || unicode.IsDigit(current) || current == '_' || current == '-':
			result.WriteRune(current)
			previousHyphen = current == '-'
		case unicode.IsSpace(current):
			if result.Len() != 0 && !previousHyphen {
				result.WriteByte('-')
				previousHyphen = true
			}
		}
	}
	return strings.Trim(result.String(), "-")
}

func generatedSource(content []byte) string {
	const prefix = "<!-- Generated from "
	start := bytes.Index(content, []byte(prefix))
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := bytes.Index(content[start:], []byte(" by "))
	if end < 0 {
		end = bytes.Index(content[start:], []byte(" -->"))
	}
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(string(content[start : start+end]))
}

func generatedOriginAllowed(repository string, source *url.URL) bool {
	if source.Hostname() == "" {
		return true
	}
	parts := strings.Split(repository, "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "github.com") {
		return false
	}
	host := strings.TrimSuffix(parts[len(parts)-1], ".git")
	return strings.HasSuffix(strings.ToLower(host), ".github.io") && strings.EqualFold(source.Hostname(), host)
}

func lineStarts(content []byte) []int {
	result := []int{0}
	for index, value := range content {
		if value == '\n' {
			result = append(result, index+1)
		}
	}
	return result
}

func (model *documentModel) sourceRange(start, end int) graph.Range {
	position := func(offset int) graph.Position {
		offset = max(0, min(offset, len(model.content)))
		line, _ := slices.BinarySearch(model.lineStarts, offset)
		if line >= len(model.lineStarts) || model.lineStarts[line] != offset {
			line--
		}
		return graph.Position{Line: int32(line), Column: int32(offset - model.lineStarts[line]), Byte: int64(offset)}
	}
	return graph.Range{Start: position(start), End: position(end)}
}

type registry struct{ id, stable, display, kind string }

type resolver struct {
	identity    string
	paths       map[string]entry
	directories map[string]bool
	documents   map[string]*documentModel
	routes      map[string]string
	registry    map[string]registry
	aliases     map[string]string
	surface     string
}

func newResolver(identity string, entries []entry, documents map[string]*documentModel) *resolver {
	result := &resolver{
		identity: identity, paths: map[string]entry{}, directories: map[string]bool{".": true},
		documents: documents, routes: map[string]string{}, registry: map[string]registry{}, aliases: map[string]string{},
	}
	var surface []string
	for _, item := range entries {
		result.paths[item.path] = item
		surface = append(surface, "path:"+item.path+"\x00"+item.kind+"\x00"+item.target)
		for directory := path.Dir(item.path); directory != "."; directory = path.Dir(directory) {
			result.directories[directory] = true
		}
	}
	for directory := range result.directories {
		surface = append(surface, "directory:"+directory)
	}
	names := make([]string, 0, len(documents))
	for name := range documents {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		document := documents[name]
		if route := normalizeRoute(document.metadata.Permalink); route != "" {
			if existing, found := result.routes[route]; found && existing != name {
				result.routes[route] = ""
			} else if !found {
				result.routes[route] = name
			}
			result.addRegistry("route", route, route)
		}
		for _, redirect := range document.metadata.RedirectFrom {
			if route := normalizeRoute(redirect); route != "" {
				if existing, found := result.routes[route]; found && existing != name {
					result.routes[route] = ""
				} else if !found {
					result.routes[route] = name
				}
				result.addRegistry("route", route, route)
				surface = append(surface, name+"->"+route)
			}
		}
		if document.metadata.Series != "" {
			result.addRegistry("series", "series:"+graph.NormalizeName(document.metadata.Series), document.metadata.Series)
		}
		for _, topic := range append(append([]string{}, document.metadata.Topics...), append(document.metadata.Tags, document.metadata.Categories...)...) {
			result.addRegistry("topic", "topic:"+graph.NormalizeName(topic), topic)
		}
		for _, heading := range document.headings {
			surface = append(surface, name+"#"+heading.anchor)
		}
		surface = append(surface, name+"="+document.metadata.Permalink)
	}
	for _, name := range names {
		document := documents[name]
		for _, link := range document.links {
			result.registerDestination(document.path, link.destination)
		}
	}
	slices.Sort(surface)
	result.surface = digest(append([]string{"resolution-surface", providerVersion}, surface...)...)
	return result
}

func (resolver *resolver) addRegistry(kind, stable, display string) string {
	id := resolver.registryID(kind, stable)
	value := registry{id: id, stable: stable, display: display, kind: kind}
	if current, ok := resolver.registry[id]; !ok || value.display < current.display {
		resolver.registry[id] = value
	}
	return id
}

func (resolver *resolver) registryID(kind, stable string) string {
	if kind == "url" {
		return stableID(kind, stable)
	}
	return stableID(kind, resolver.identity, stable)
}

func (resolver *resolver) registries() []registry {
	result := make([]registry, 0, len(resolver.registry))
	for _, value := range resolver.registry {
		result = append(result, value)
	}
	slices.SortFunc(result, func(a, b registry) int { return strings.Compare(a.id, b.id) })
	return result
}

func (resolver *resolver) registerDestination(source, raw string) {
	destination, supported := normalizeLiquid(raw)
	if !supported {
		return
	}
	if strings.HasPrefix(destination, "//") {
		destination = "https:" + destination
	}
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Scheme == "" || parsed.Scheme == "file" || parsed.Scheme == "data" || parsed.Scheme == "javascript" {
		return
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		urlID := resolver.addRegistry("url", "url:"+parsed.String(), parsed.String())
		if target, ok := githubTarget(parsed); ok {
			resolver.aliases[urlID] = target
		}
	}
}

type resolution struct {
	id       string
	evidence graph.Evidence
}

func (resolver *resolver) resolve(source, raw string) (resolution, bool) {
	destination, supported := normalizeLiquid(raw)
	if !supported {
		return resolution{}, false
	}
	if strings.HasPrefix(destination, "//") {
		destination = "https:" + destination
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return resolution{}, false
	}
	if parsed.Scheme != "" {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return resolution{}, false
		}
		return resolution{resolver.registryID("url", "url:"+parsed.String()), graph.EvidenceDeclared}, true
	}
	fragment := parsed.Fragment
	pathname, err := url.PathUnescape(parsed.Path)
	if err != nil || strings.ContainsRune(pathname, 0) || strings.Contains(pathname, `\`) {
		return resolution{}, false
	}
	var target string
	if pathname == "" {
		target = source
	} else if strings.HasPrefix(pathname, "/") {
		if route, ok := resolver.routes[normalizeRoute(pathname)]; ok {
			if route == "" {
				return resolution{}, false
			}
			target = route
		} else {
			target = strings.TrimPrefix(path.Clean(pathname), "/")
		}
	} else {
		target = path.Clean(path.Join(path.Dir(source), pathname))
	}
	if target == ".." || strings.HasPrefix(target, "../") || strings.HasPrefix(target, "/") {
		return resolution{}, false
	}
	if target == "" || target == "." {
		return resolution{workspaceSymbolID(resolver.identity), graph.EvidenceDeclared}, true
	}
	if document, ok := resolver.documents[target]; ok {
		if fragment != "" {
			anchor := headingAnchor(fragment)
			for _, heading := range document.headings {
				if heading.addressable && (heading.anchor == fragment || heading.anchor == anchor) {
					return resolution{heading.id, graph.EvidenceDeclared}, true
				}
			}
			return resolution{}, false
		}
		return resolution{fileSymbolID(resolver.identity, target), graph.EvidenceDeclared}, true
	}
	if _, ok := resolver.paths[target]; ok {
		if fragment != "" {
			return resolution{}, false
		}
		return resolution{fileSymbolID(resolver.identity, target), graph.EvidenceDeclared}, true
	}
	if resolver.directories[strings.TrimSuffix(target, "/")] {
		if fragment != "" {
			return resolution{}, false
		}
		return resolution{directorySymbolID(resolver.identity, strings.TrimSuffix(target, "/")), graph.EvidenceDeclared}, true
	}
	return resolution{}, false
}

func githubTarget(parsed *url.URL) (string, bool) {
	if !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.RawPath != "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || !validGitHubSegment(parts[0]) || !validGitHubSegment(strings.TrimSuffix(parts[1], ".git")) {
		return "", false
	}
	identity := "github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if len(parts) == 2 && parsed.Fragment == "" {
		return workspaceSymbolID(identity), true
	}
	if len(parts) >= 5 && parts[2] == "blob" && unambiguousGitHubRef(parts[3]) {
		name := strings.Join(parts[4:], "/")
		if !validRepositoryPath(name) {
			return "", false
		}
		if parsed.Fragment != "" && isMarkdown(name) {
			return sectionSymbolID(identity, name, headingAnchor(parsed.Fragment)), true
		}
		return fileSymbolID(identity, name), true
	}
	if len(parts) >= 5 && parts[2] == "tree" && unambiguousGitHubRef(parts[3]) {
		name := strings.Join(parts[4:], "/")
		if !validRepositoryPath(name) {
			return "", false
		}
		return directorySymbolID(identity, name), true
	}
	return "", false
}

func validGitHubSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, current := range value {
		if !((current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') ||
			(current >= '0' && current <= '9') || current == '-' || current == '_' || current == '.') {
			return false
		}
	}
	return true
}

func unambiguousGitHubRef(value string) bool {
	if value == "main" || value == "master" {
		return true
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, current := range value {
		if !((current >= '0' && current <= '9') || (current >= 'a' && current <= 'f') || (current >= 'A' && current <= 'F')) {
			return false
		}
	}
	return true
}

func normalizeLiquid(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "{{") {
		return value, true
	}
	match := relativeURLExpression.FindStringSubmatch(value)
	if len(match) != 4 || match[1] != match[3] {
		return value, false
	}
	return match[2], true
}

func normalizeRoute(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	if parsed.Path != "" {
		value = parsed.Path
	}
	value = "/" + strings.TrimPrefix(path.Clean("/"+value), "/")
	if strings.HasSuffix(value, "/") || path.Ext(value) == "" {
		value = strings.TrimSuffix(value, "/") + "/"
	}
	return value
}

func (model *documentModel) facts(resolver *resolver) graph.UnitFacts {
	unitID := stableID("unit", model.repository, model.path)
	documentID := stableID("document", model.repository, model.path)
	rootID := fileSymbolID(model.repository, model.path)
	contentDigest := sha256.Sum256(model.content)
	input := digest("markdown", providerVersion, model.path, string(model.content), resolver.surface)
	facts := graph.UnitFacts{
		Unit: graph.Unit{
			ID: unitID, Provider: providerName, ProviderVersion: providerVersion, Language: "markdown", Variant: "document",
			InputFingerprint: input, SurfaceFingerprint: "sha256:" + hex.EncodeToString(contentDigest[:]),
		},
		Documents: []graph.Document{{
			ID: documentID, UnitID: unitID, Path: model.path, Language: "markdown", ContentHash: "sha256:" + hex.EncodeToString(contentDigest[:]),
			Provider: providerName, ProviderVersion: providerVersion,
		}},
	}
	title := model.metadata.Title
	if title == "" {
		title = path.Base(model.path)
	}
	zero := model.sourceRange(0, 0)
	contentEvidence := graph.EvidenceSyntactic
	base := strings.ToLower(path.Base(model.path))
	if model.generatedFrom != "" || base == "llms.txt" || base == "llms-full.txt" {
		contentEvidence = graph.EvidenceGenerated
	}
	facts.Symbols = append(facts.Symbols, graph.Symbol{
		ID: rootID, UnitID: unitID, StableName: model.path, DisplayName: title, SearchTerms: model.preludeSearchTerms(), Kind: "document", DocumentID: documentID,
		Definition: zero, Provider: providerName, Evidence: contentEvidence,
	})
	facts.Occurrences = append(facts.Occurrences, graph.Occurrence{
		ID: stableID("occurrence", unitID, rootID, "definition"), UnitID: unitID, SymbolID: rootID, DocumentID: documentID,
		Role: "definition", Range: zero, Provider: providerName, Evidence: contentEvidence,
	})
	for index, heading := range model.headings {
		sourceRange := model.sourceRange(heading.start, heading.end)
		facts.Symbols = append(facts.Symbols, graph.Symbol{
			ID: heading.id, UnitID: unitID, StableName: model.path + "#" + heading.anchor, DisplayName: heading.title, SearchTerms: model.headingSearchTerms(index), Kind: "section",
			DocumentID: documentID, Definition: sourceRange, Provider: providerName, Evidence: contentEvidence,
		})
		facts.Occurrences = append(facts.Occurrences, graph.Occurrence{
			ID: stableID("occurrence", unitID, heading.id, "definition"), UnitID: unitID, SymbolID: heading.id, DocumentID: documentID,
			Role: "definition", Range: sourceRange, Provider: providerName, Evidence: contentEvidence,
		})
		facts.Edges = append(facts.Edges, sourceEdge(unitID, heading.parent, heading.id, graph.EdgeContains, contentEvidence, documentID, sourceRange))
	}
	for _, block := range model.blocks {
		sourceRange := model.sourceRange(block.start, block.end)
		display := fmt.Sprintf("code block %d", block.ordinal)
		if block.language != "" {
			display += " (" + block.language + ")"
		}
		facts.Symbols = append(facts.Symbols, graph.Symbol{
			ID: block.id, UnitID: unitID, StableName: fmt.Sprintf("%s#code-%d", model.path, block.ordinal), DisplayName: display,
			SearchTerms: graph.ExtractSearchTerms(string(model.content[max(0, block.start):min(len(model.content), block.end)])),
			Kind:        "code-block", DocumentID: documentID, Definition: sourceRange, Provider: providerName, Evidence: contentEvidence,
		})
		facts.Edges = append(facts.Edges, sourceEdge(unitID, block.parent, block.id, graph.EdgeContains, contentEvidence, documentID, sourceRange))
	}
	for _, link := range model.links {
		target, resolved := resolver.resolve(model.path, link.destination)
		if !resolved {
			continue
		}
		kind, role := graph.EdgeLinksTo, "link"
		if link.embed {
			kind, role = graph.EdgeEmbeds, "embed"
		}
		sourceRange := model.sourceRange(link.start, link.end)
		source := model.sectionAt(link.start)
		facts.Edges = append(facts.Edges, sourceEdge(unitID, source, target.id, kind, target.evidence, documentID, sourceRange))
		facts.Occurrences = append(facts.Occurrences, graph.Occurrence{
			ID: stableID("occurrence", unitID, target.id, role, fmt.Sprintf("%d", link.start)), UnitID: unitID, SymbolID: target.id,
			DocumentID: documentID, Role: role, Range: sourceRange, Provider: providerName, Evidence: target.evidence,
		})
	}
	if route := normalizeRoute(model.metadata.Permalink); route != "" {
		facts.Edges = append(facts.Edges, sourceEdge(unitID, rootID, resolver.registryID("route", route), graph.EdgeExposes, graph.EvidenceDeclared, documentID, zero))
	}
	for _, redirect := range model.metadata.RedirectFrom {
		if route := normalizeRoute(redirect); route != "" {
			facts.Edges = append(facts.Edges, sourceEdge(unitID, rootID, resolver.registryID("route", route), graph.EdgeExposes, graph.EvidenceDeclared, documentID, zero))
		}
	}
	if model.metadata.Series != "" {
		facts.Edges = append(facts.Edges, sourceEdge(unitID, rootID, resolver.registryID("series", "series:"+graph.NormalizeName(model.metadata.Series)), graph.EdgeMemberOf, graph.EvidenceDeclared, documentID, zero))
	}
	for _, topic := range append(append([]string{}, model.metadata.Topics...), append(model.metadata.Tags, model.metadata.Categories...)...) {
		facts.Edges = append(facts.Edges, sourceEdge(unitID, rootID, resolver.registryID("topic", "topic:"+graph.NormalizeName(topic)), graph.EdgeMemberOf, graph.EvidenceDeclared, documentID, zero))
	}
	if model.generatedFrom != "" {
		if parsed, err := url.Parse(model.generatedFrom); err == nil && generatedOriginAllowed(model.repository, parsed) {
			if source, ok := resolver.routes[normalizeRoute(parsed.Path)]; ok && source != "" {
				facts.Edges = append(facts.Edges, sourceEdge(unitID, fileSymbolID(model.repository, source), rootID, graph.EdgeGenerates, graph.EvidenceGenerated, documentID, zero))
			}
		}
	}
	slices.SortFunc(facts.Symbols, func(a, b graph.Symbol) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(facts.Occurrences, func(a, b graph.Occurrence) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(facts.Edges, graph.CompareEdges)
	facts.Edges = slices.CompactFunc(facts.Edges, func(a, b graph.Edge) bool { return a.ID == b.ID })
	return facts
}

func sourceEdge(unitID, from, to string, kind graph.EdgeKind, evidence graph.Evidence, documentID string, sourceRange graph.Range) graph.Edge {
	return (relationship.Builder{UnitID: unitID, Provider: providerName, Evidence: evidence}).MustBuild(relationship.Spec{
		ID:   stableID("edge", unitID, from, string(kind), to, fmt.Sprintf("%d", sourceRange.Start.Byte)),
		From: from, To: to, Kind: kind, DocumentID: documentID, Range: sourceRange,
	})
}
