package schemabuild

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

const maxGraphQLTokens = 500_000

func parseGraphQL(builder *factBuilder, files []sourceFile) ([]string, error) {
	byPath := make(map[string]sourceFile, len(files))
	var schemaSources []*ast.Source
	queryDocuments := map[string]*ast.QueryDocument{}
	for _, file := range files {
		byPath[file.path] = file
		source := &ast.Source{Name: file.path, Input: string(file.data)}
		schemaDocument, schemaErr := parser.ParseSchemaWithLimit(source, maxGraphQLTokens)
		if schemaErr == nil && schemaDocumentCount(schemaDocument) != 0 {
			schemaSources = append(schemaSources, source)
			continue
		}
		queryDocument, queryErr := parser.ParseQueryWithTokenLimit(source, maxGraphQLTokens)
		if queryErr == nil && queryDocumentCount(queryDocument) != 0 {
			queryDocuments[file.path] = queryDocument
			continue
		}
		if schemaErr == nil && queryErr == nil {
			continue // comments or whitespace only
		}
		return nil, fmt.Errorf("parse %s as GraphQL SDL or executable document: SDL: %v; executable: %v", file.path, schemaErr, queryErr)
	}

	var schema *ast.Schema
	if len(schemaSources) != 0 {
		var err error
		schema, err = gqlparser.LoadSchema(schemaSources...)
		if err != nil {
			return nil, fmt.Errorf("validate GraphQL schema: %w", err)
		}
	}
	schemaDocument, err := parser.ParseSchemasWithLimit(maxGraphQLTokens, schemaSources...)
	if err != nil {
		return nil, fmt.Errorf("parse combined GraphQL schema: %w", err)
	}
	definitions := append(append(ast.DefinitionList{}, schemaDocument.Definitions...), schemaDocument.Extensions...)
	definedTypes := map[string]bool{}
	for _, definition := range definitions {
		definedTypes[definition.Name] = true
	}
	for _, definition := range definitions {
		file := graphqlPositionFile(definition.Position)
		rng := graphqlRange(byPath[file].data, definition.Position, len(definition.Name))
		typeID := builder.addSymbol("graphql/type", definition.Name, definition.Name, graphqlDefinitionKind(definition.Kind), file, rng, graph.EvidenceDeclared)
		for _, interfaceName := range definition.Interfaces {
			target := graphqlTypeTarget(builder, interfaceName, definedTypes)
			builder.addReference(target, file, rng, graph.EvidenceDeclared)
			builder.addEdge(typeID, target, graph.EdgeImplements, file, rng, graph.EvidenceDeclared)
		}
		for index, member := range definition.Types {
			memberRange := rng
			if index < len(definition.TypePositions) {
				memberRange = graphqlRange(byPath[file].data, definition.TypePositions[index], len(member))
			}
			target := graphqlTypeTarget(builder, member, definedTypes)
			builder.addReference(target, file, memberRange, graph.EvidenceDeclared)
			builder.addEdge(typeID, target, graph.EdgeReferences, file, memberRange, graph.EvidenceDeclared)
		}
		for _, field := range definition.Fields {
			fieldRange := graphqlRange(byPath[file].data, field.Position, len(field.Name))
			fieldID := builder.addSymbol("graphql/field", definition.Name+"."+field.Name, field.Name, "graphql-field", file, fieldRange, graph.EvidenceDeclared)
			builder.addEdge(typeID, fieldID, graph.EdgeContains, file, fieldRange, graph.EvidenceDeclared)
			addGraphQLTypeRef(builder, file, fieldID, field.Type, fieldRange, definedTypes)
			for _, argument := range field.Arguments {
				argumentRange := graphqlRange(byPath[file].data, argument.Position, len(argument.Name))
				argumentID := builder.addSymbol("graphql/argument", definition.Name+"."+field.Name+"("+argument.Name+")", argument.Name, "graphql-argument", file, argumentRange, graph.EvidenceDeclared)
				builder.addEdge(fieldID, argumentID, graph.EdgeContains, file, argumentRange, graph.EvidenceDeclared)
				addGraphQLTypeRef(builder, file, argumentID, argument.Type, argumentRange, definedTypes)
			}
		}
		for _, value := range definition.EnumValues {
			valueRange := graphqlRange(byPath[file].data, value.Position, len(value.Name))
			valueID := builder.addSymbol("graphql/enum-value", definition.Name+"."+value.Name, value.Name, "graphql-enum-value", file, valueRange, graph.EvidenceDeclared)
			builder.addEdge(typeID, valueID, graph.EdgeContains, file, valueRange, graph.EvidenceDeclared)
		}
	}

	fragments := map[string]string{}
	for file, document := range queryDocuments {
		for _, fragment := range document.Fragments {
			rng := graphqlRange(byPath[file].data, fragment.Position, len(fragment.Name))
			fragments[fragment.Name] = builder.addSymbol("graphql/fragment", fragment.Name, fragment.Name, "graphql-fragment", file, rng, graph.EvidenceDeclared)
		}
	}
	var warnings []string
	for file, document := range queryDocuments {
		resolved := false
		if schema != nil {
			if problems := validator.Validate(schema, document); len(problems) == 0 {
				resolved = true
			} else {
				warnings = append(warnings, file+": executable validation unavailable; field relationships remain syntactic: "+problems.Error())
			}
		}
		for index, operation := range document.Operations {
			name := operation.Name
			if name == "" {
				name = fmt.Sprintf("anonymous-%s-%d", operation.Operation, index+1)
			}
			rng := graphqlRange(byPath[file].data, operation.Position, len(name))
			operationID := builder.addSymbol("graphql/operation", file+"#"+name, name, "graphql-operation-"+string(operation.Operation), file, rng, graph.EvidenceDeclared)
			for _, variable := range operation.VariableDefinitions {
				addGraphQLTypeRef(builder, file, operationID, variable.Type, graphqlRange(byPath[file].data, variable.Position, len(variable.Variable)), definedTypes)
			}
			addGraphQLSelections(builder, file, operationID, operation.SelectionSet, fragments, resolved, byPath[file].data, definedTypes)
		}
		for _, fragment := range document.Fragments {
			fragmentID := fragments[fragment.Name]
			rng := graphqlRange(byPath[file].data, fragment.Position, len(fragment.Name))
			if fragment.TypeCondition != "" {
				target := graphqlTypeTarget(builder, fragment.TypeCondition, definedTypes)
				builder.addReference(target, file, rng, graph.EvidenceDeclared)
				builder.addEdge(fragmentID, target, graph.EdgeReferences, file, rng, graph.EvidenceDeclared)
			}
			addGraphQLSelections(builder, file, fragmentID, fragment.SelectionSet, fragments, resolved, byPath[file].data, definedTypes)
		}
	}
	slices.Sort(warnings)
	return warnings, nil
}

func schemaDocumentCount(document *ast.SchemaDocument) int {
	if document == nil {
		return 0
	}
	return len(document.Schema) + len(document.SchemaExtension) + len(document.Directives) + len(document.Definitions) + len(document.Extensions)
}

func queryDocumentCount(document *ast.QueryDocument) int {
	if document == nil {
		return 0
	}
	return len(document.Operations) + len(document.Fragments)
}

func graphqlDefinitionKind(kind ast.DefinitionKind) string {
	return "graphql-" + strings.ToLower(strings.ReplaceAll(string(kind), "_", "-"))
}

func graphqlPositionFile(position *ast.Position) string {
	if position == nil || position.Src == nil {
		return ""
	}
	return position.Src.Name
}

func graphqlRange(data []byte, position *ast.Position, tokenLength int) graph.Range {
	if position == nil {
		return unknownRange()
	}
	start := runeOffsetToByte(data, position.Start)
	end := runeOffsetToByte(data, position.End)
	if end <= start {
		end = min(start+tokenLength, len(data))
	}
	return byteRange(data, start, end)
}

func runeOffsetToByte(data []byte, runeOffset int) int {
	if runeOffset < 0 {
		return 0
	}
	byteOffset := 0
	for count := 0; count < runeOffset && byteOffset < len(data); count++ {
		_, size := utf8.DecodeRune(data[byteOffset:])
		byteOffset += size
	}
	return byteOffset
}

func addGraphQLTypeRef(builder *factBuilder, file, owner string, graphqlType *ast.Type, rng graph.Range, definedTypes map[string]bool) {
	if graphqlType == nil || graphqlType.NamedType == "" {
		return
	}
	target := graphqlTypeTarget(builder, graphqlType.NamedType, definedTypes)
	builder.addReference(target, file, rng, graph.EvidenceDeclared)
	builder.addEdge(owner, target, graph.EdgeReferences, file, rng, graph.EvidenceDeclared)
}

func graphqlTypeTarget(builder *factBuilder, name string, definedTypes map[string]bool) string {
	if definedTypes[name] {
		return builder.localID("graphql/type", name)
	}
	return openID("graphql/type", name)
}

func addGraphQLSelections(builder *factBuilder, file, owner string, selections ast.SelectionSet, fragments map[string]string, resolved bool, data []byte, definedTypes map[string]bool) {
	for _, selection := range selections {
		switch value := selection.(type) {
		case *ast.Field:
			rng := graphqlRange(data, value.Position, len(value.Name))
			if resolved && value.ObjectDefinition != nil && value.Definition != nil {
				target := builder.localID("graphql/field", value.ObjectDefinition.Name+"."+value.Definition.Name)
				builder.addReference(target, file, rng, graph.EvidenceSyntactic)
				builder.addEdge(owner, target, graph.EdgeReferences, file, rng, graph.EvidenceSyntactic)
			}
			addGraphQLSelections(builder, file, owner, value.SelectionSet, fragments, resolved, data, definedTypes)
		case *ast.FragmentSpread:
			rng := graphqlRange(data, value.Position, len(value.Name))
			target := fragments[value.Name]
			if target == "" {
				target = openID("graphql/fragment", value.Name)
			}
			builder.addReference(target, file, rng, graph.EvidenceSyntactic)
			builder.addEdge(owner, target, graph.EdgeReferences, file, rng, graph.EvidenceSyntactic)
		case *ast.InlineFragment:
			if value.TypeCondition != "" {
				rng := graphqlRange(data, value.Position, len(value.TypeCondition))
				target := graphqlTypeTarget(builder, value.TypeCondition, definedTypes)
				builder.addReference(target, file, rng, graph.EvidenceSyntactic)
				builder.addEdge(owner, target, graph.EdgeReferences, file, rng, graph.EvidenceSyntactic)
			}
			addGraphQLSelections(builder, file, owner, value.SelectionSet, fragments, resolved, data, definedTypes)
		}
	}
}
