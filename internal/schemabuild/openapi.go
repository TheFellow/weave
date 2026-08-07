package schemabuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/getkin/kin-openapi/openapi3"
	"go.yaml.in/yaml/v3"
)

type openAPIDocument struct {
	file sourceFile
	root *yaml.Node
	doc  *openapi3.T
}

func parseOpenAPI(builder *factBuilder, files []sourceFile) ([]string, error) {
	documents := make(map[string]openAPIDocument, len(files))
	var roots []string
	for _, file := range files {
		decoder := yaml.NewDecoder(bytes.NewReader(file.data))
		decoder.KnownFields(false)
		var tree yaml.Node
		if err := decoder.Decode(&tree); err != nil {
			return nil, fmt.Errorf("parse %s with YAML parser: %w", file.path, err)
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); err == nil {
			return nil, fmt.Errorf("%s contains multiple YAML documents", file.path)
		} else if err != io.EOF {
			return nil, fmt.Errorf("finish parsing %s with YAML parser: %w", file.path, err)
		}
		if len(tree.Content) != 1 || tree.Content[0].Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s is not an OpenAPI mapping", file.path)
		}
		root := tree.Content[0]
		item := openAPIDocument{file: file, root: root}
		if versionNode := mappingValue(root, "openapi"); versionNode != nil {
			var plain any
			if err := root.Decode(&plain); err != nil {
				return nil, fmt.Errorf("decode %s: %w", file.path, err)
			}
			encoded, err := json.Marshal(plain)
			if err != nil {
				return nil, fmt.Errorf("normalize %s: %w", file.path, err)
			}
			var model openapi3.T
			if err := json.Unmarshal(encoded, &model); err != nil {
				return nil, fmt.Errorf("parse %s with kin-openapi model: %w", file.path, err)
			}
			if model.OpenAPIMajorMinor() == "" {
				return nil, fmt.Errorf("%s uses unsupported OpenAPI version %q (only OpenAPI 3.x is indexed)", file.path, model.OpenAPI)
			}
			if model.Info == nil {
				return nil, fmt.Errorf("%s has no required info object", file.path)
			}
			item.doc = &model
			roots = append(roots, file.path)
		}
		documents[file.path] = item
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no OpenAPI 3 root document found")
	}
	slices.Sort(roots)
	// Register component identities before walking operations so a forward $ref
	// cannot win canonical symbol metadata with a generic ref-target kind.
	for _, name := range roots {
		item := documents[name]
		for _, schema := range mappingPairs(mappingValue(mappingValue(item.root, "components"), "schemas")) {
			pointer := "#/components/schemas/" + escapeJSONPointer(schema.key.Value)
			builder.addSymbol("openapi/pointer", name+pointer, schema.key.Value, "openapi-schema", name, nodeRange(item.file.data, schema.key), graph.EvidenceDeclared)
		}
	}
	var warnings []string
	for _, name := range roots {
		item := documents[name]
		documentID := builder.addSymbol("openapi/document", name, openAPITitle(item), "openapi-document", name, nodeRange(item.file.data, mappingValue(item.root, "openapi")), graph.EvidenceDeclared)
		paths := mappingValue(item.root, "paths")
		if paths != nil && paths.Kind != yaml.MappingNode {
			return warnings, fmt.Errorf("%s paths is not a mapping", name)
		}
		for _, pair := range mappingPairs(paths) {
			pathName, pathNode := pair.key.Value, pair.value
			pathID := builder.addSymbol("openapi/path", name+"#path:"+pathName, pathName, "openapi-path", name, nodeRange(item.file.data, pair.key), graph.EvidenceDeclared)
			builder.addEdge(documentID, pathID, graph.EdgeContains, name, nodeRange(item.file.data, pair.key), graph.EvidenceDeclared)
			for _, operation := range mappingPairs(pathNode) {
				method := strings.ToUpper(operation.key.Value)
				if !isHTTPMethod(method) || operation.value.Kind != yaml.MappingNode {
					continue
				}
				display := method + " " + pathName
				if operationID := mappingValue(operation.value, "operationId"); operationID != nil && operationID.Value != "" {
					display = operationID.Value
				}
				operationID := builder.addSymbol("openapi/operation", name+"#operation:"+method+":"+pathName, display, "openapi-operation", name, nodeRange(item.file.data, operation.key), graph.EvidenceDeclared)
				builder.addEdge(pathID, operationID, graph.EdgeContains, name, nodeRange(item.file.data, operation.key), graph.EvidenceDeclared)
				warnings = append(warnings, addOpenAPIRefs(builder, documents, name, operationID, operation.value)...)
			}
			warnings = append(warnings, addOpenAPIRefs(builder, documents, name, pathID, pathNode)...)
		}
		components := mappingValue(item.root, "components")
		schemas := mappingValue(components, "schemas")
		for _, schema := range mappingPairs(schemas) {
			pointer := "#/components/schemas/" + escapeJSONPointer(schema.key.Value)
			schemaID := builder.addSymbol("openapi/pointer", name+pointer, schema.key.Value, "openapi-schema", name, nodeRange(item.file.data, schema.key), graph.EvidenceDeclared)
			builder.addEdge(documentID, schemaID, graph.EdgeContains, name, nodeRange(item.file.data, schema.key), graph.EvidenceDeclared)
			properties := mappingValue(schema.value, "properties")
			for _, property := range mappingPairs(properties) {
				propertyID := builder.addSymbol("openapi/property", name+pointer+"/properties/"+escapeJSONPointer(property.key.Value), property.key.Value, "openapi-property", name, nodeRange(item.file.data, property.key), graph.EvidenceDeclared)
				builder.addEdge(schemaID, propertyID, graph.EdgeContains, name, nodeRange(item.file.data, property.key), graph.EvidenceDeclared)
				warnings = append(warnings, addOpenAPIRefs(builder, documents, name, propertyID, property.value)...)
			}
			warnings = append(warnings, addOpenAPIRefs(builder, documents, name, schemaID, schema.value)...)
		}
		warnings = append(warnings, addOpenAPIRefs(builder, documents, name, documentID, item.root)...)
	}
	slices.Sort(warnings)
	warnings = slices.Compact(warnings)
	return warnings, nil
}

func openAPITitle(item openAPIDocument) string {
	if item.doc != nil && item.doc.Info != nil && item.doc.Info.Title != "" {
		return item.doc.Info.Title
	}
	return item.file.path
}

type yamlPair struct{ key, value *yaml.Node }

func mappingPairs(node *yaml.Node) []yamlPair {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	result := make([]yamlPair, 0, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		result = append(result, yamlPair{key: node.Content[index], value: node.Content[index+1]})
	}
	return result
}

func mappingValue(node *yaml.Node, name string) *yaml.Node {
	for _, pair := range mappingPairs(node) {
		if pair.key.Value == name {
			return pair.value
		}
	}
	return nil
}

func nodeRange(data []byte, node *yaml.Node) graph.Range {
	if node == nil {
		return unknownRange()
	}
	return lineColumnRange(data, node.Line, node.Column, len(node.Value))
}

func isHTTPMethod(value string) bool {
	switch value {
	case "GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE":
		return true
	default:
		return false
	}
}

func addOpenAPIRefs(builder *factBuilder, documents map[string]openAPIDocument, current, owner string, node *yaml.Node) []string {
	var warnings []string
	var walk func(*yaml.Node)
	walk = func(value *yaml.Node) {
		if value == nil {
			return
		}
		if value.Kind == yaml.MappingNode {
			for _, pair := range mappingPairs(value) {
				if pair.key.Value == "$ref" && pair.value.Kind == yaml.ScalarNode {
					target, warning := openAPIRefTarget(builder, documents, current, pair.value.Value)
					rng := nodeRange(documents[current].file.data, pair.value)
					builder.addReference(target, current, rng, graph.EvidenceDeclared)
					builder.addEdge(owner, target, graph.EdgeReferences, current, rng, graph.EvidenceDeclared)
					if warning != "" {
						warnings = append(warnings, warning)
					}
				}
				walk(pair.value)
			}
			return
		}
		for _, child := range value.Content {
			walk(child)
		}
	}
	walk(node)
	return warnings
}

func openAPIRefTarget(builder *factBuilder, documents map[string]openAPIDocument, current, reference string) (string, string) {
	parsed, err := url.Parse(reference)
	if err != nil {
		return openID("openapi/ref", reference), "malformed $ref remains an open endpoint: " + reference
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		return openID("openapi/ref", parsed.String()), "remote $ref left unresolved without network access: " + reference
	}
	targetPath := current
	if parsed.Path != "" {
		if strings.HasPrefix(parsed.Path, "/") || strings.ContainsRune(parsed.Path, '\\') {
			return openID("openapi/ref", current+"->"+reference), "non-contained local $ref left unresolved: " + reference
		}
		targetPath = path.Clean(path.Join(path.Dir(current), parsed.Path))
		if !validPath(targetPath) || strings.HasPrefix(targetPath, "../") {
			return openID("openapi/ref", current+"->"+reference), "escaping local $ref left unresolved: " + reference
		}
	}
	fragment := "#" + parsed.Fragment
	if parsed.Fragment == "" {
		fragment = "#"
	}
	targetDoc, local := documents[targetPath]
	if !local {
		return openID("openapi/ref", targetPath+fragment), "Git-visible local $ref target is not an indexed OpenAPI source: " + reference
	}
	stable := targetPath + fragment
	targetNode := resolveYAMLPointer(targetDoc.root, parsed.Fragment)
	if targetNode == nil {
		return openID("openapi/ref", stable), "local $ref fragment was not found and remains an open endpoint: " + reference
	}
	rng := nodeRange(targetDoc.file.data, targetNode)
	display := reference
	if targetNode != nil && targetNode.Value != "" {
		display = targetNode.Value
	}
	target := builder.addSymbol("openapi/pointer", stable, display, "openapi-schema-ref-target", targetPath, rng, graph.EvidenceDeclared)
	return target, ""
}

func resolveYAMLPointer(root *yaml.Node, fragment string) *yaml.Node {
	if fragment == "" {
		return root
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil
	}
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		current = mappingValue(current, part)
		if current == nil {
			return nil
		}
	}
	return current
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
