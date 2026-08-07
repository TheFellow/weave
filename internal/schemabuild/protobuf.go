package schemabuild

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func parseProtobuf(ctx context.Context, builder *factBuilder, files []sourceFile) ([]string, error) {
	sources := make(map[string]string, len(files))
	paths := make([]string, 0, len(files))
	contents := make(map[string][]byte, len(files))
	for _, file := range files {
		sources[file.path] = string(file.data)
		contents[file.path] = file.data
		paths = append(paths, file.path)
	}
	compiler := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)}),
		MaxParallelism: 2,
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	compiled, err := compiler.Compile(ctx, paths...)
	if err != nil {
		return nil, fmt.Errorf("Buf protocompile linking failed: %w", err)
	}
	localFiles := map[string]bool{}
	for _, name := range paths {
		localFiles[name] = true
	}
	for _, file := range compiled {
		name := file.Path()
		if !localFiles[name] {
			continue
		}
		data := contents[name]
		fileID := builder.addSymbol("protobuf/file", name, name, "protobuf-file", name, byteRange(data, 0, 0), graph.EvidenceDeclared)
		packageName := string(file.Package())
		var packageID string
		if packageName != "" {
			packageID = builder.addSymbol("protobuf/package", packageName, packageName, "protobuf-package", name, findTokenRange(data, packageName, 0), graph.EvidenceDeclared)
			builder.addEdge(fileID, packageID, graph.EdgeMemberOf, name, unknownRange(), graph.EvidenceDeclared)
		}
		imports := file.Imports()
		for index := range imports.Len() {
			imported := imports.Get(index).Path()
			target := openID("protobuf/file", imported)
			if localFiles[imported] {
				target = builder.localID("protobuf/file", imported)
			}
			rng := findTokenRange(data, imported, 0)
			builder.addReference(target, name, rng, graph.EvidenceDeclared)
			builder.addEdge(fileID, target, graph.EdgeImports, name, rng, graph.EvidenceDeclared)
			builder.addEdge(fileID, target, graph.EdgeDependsOn, name, rng, graph.EvidenceDeclared)
		}
		messages := file.Messages()
		for index := range messages.Len() {
			addProtoMessage(builder, file, messages.Get(index), name, fileID, localFiles)
		}
		enums := file.Enums()
		for index := range enums.Len() {
			addProtoEnum(builder, file, enums.Get(index), name, fileID)
		}
		services := file.Services()
		for index := range services.Len() {
			service := services.Get(index)
			serviceID := protoDescriptorSymbol(builder, file, service, name, "protobuf-service")
			builder.addEdge(parentOr(fileID, packageID), serviceID, graph.EdgeContains, name, protoRange(file, service), graph.EvidenceDeclared)
			methods := service.Methods()
			for methodIndex := range methods.Len() {
				method := methods.Get(methodIndex)
				methodID := protoDescriptorSymbol(builder, file, method, name, "protobuf-rpc")
				rng := protoRange(file, method)
				builder.addEdge(serviceID, methodID, graph.EdgeContains, name, rng, graph.EvidenceDeclared)
				for _, target := range []protoreflect.MessageDescriptor{method.Input(), method.Output()} {
					targetID := protoTypeTarget(builder, target, localFiles)
					builder.addReference(targetID, name, rng, graph.EvidenceDeclared)
					builder.addEdge(methodID, targetID, graph.EdgeReferences, name, rng, graph.EvidenceDeclared)
				}
			}
		}
	}
	return nil, nil
}

func parentOr(fileID, packageID string) string {
	if packageID != "" {
		return packageID
	}
	return fileID
}

func protoDescriptorSymbol(builder *factBuilder, file protoreflect.FileDescriptor, descriptor protoreflect.Descriptor, name, kind string) string {
	stable := string(descriptor.FullName())
	return builder.addSymbol("protobuf/type", stable, string(descriptor.Name()), kind, name, protoRange(file, descriptor), graph.EvidenceDeclared)
}

func addProtoMessage(builder *factBuilder, file protoreflect.FileDescriptor, message protoreflect.MessageDescriptor, name, parent string, localFiles map[string]bool) {
	if message.IsMapEntry() {
		return
	}
	messageID := protoDescriptorSymbol(builder, file, message, name, "protobuf-message")
	builder.addEdge(parent, messageID, graph.EdgeContains, name, protoRange(file, message), graph.EvidenceDeclared)
	fields := message.Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		fieldID := protoDescriptorSymbol(builder, file, field, name, "protobuf-field")
		rng := protoRange(file, field)
		builder.addEdge(messageID, fieldID, graph.EdgeContains, name, rng, graph.EvidenceDeclared)
		var target string
		if field.IsMap() {
			value := field.MapValue()
			switch value.Kind() {
			case protoreflect.MessageKind, protoreflect.GroupKind:
				target = protoTypeTarget(builder, value.Message(), localFiles)
			case protoreflect.EnumKind:
				target = protoTypeTarget(builder, value.Enum(), localFiles)
			default:
				target = openID("protobuf/scalar", value.Kind().String())
			}
		} else {
			switch field.Kind() {
			case protoreflect.MessageKind, protoreflect.GroupKind:
				target = protoTypeTarget(builder, field.Message(), localFiles)
			case protoreflect.EnumKind:
				target = protoTypeTarget(builder, field.Enum(), localFiles)
			default:
				target = openID("protobuf/scalar", field.Kind().String())
			}
		}
		builder.addReference(target, name, rng, graph.EvidenceDeclared)
		builder.addEdge(fieldID, target, graph.EdgeReferences, name, rng, graph.EvidenceDeclared)
	}
	nested := message.Messages()
	for index := range nested.Len() {
		addProtoMessage(builder, file, nested.Get(index), name, messageID, localFiles)
	}
	enums := message.Enums()
	for index := range enums.Len() {
		addProtoEnum(builder, file, enums.Get(index), name, messageID)
	}
}

func protoTypeTarget(builder *factBuilder, descriptor protoreflect.Descriptor, localFiles map[string]bool) string {
	if descriptor != nil && descriptor.ParentFile() != nil && localFiles[descriptor.ParentFile().Path()] {
		return builder.localID("protobuf/type", string(descriptor.FullName()))
	}
	if descriptor == nil {
		return openID("protobuf/type", "unknown")
	}
	return openID("protobuf/type", string(descriptor.FullName()))
}

func addProtoEnum(builder *factBuilder, file protoreflect.FileDescriptor, enum protoreflect.EnumDescriptor, name, parent string) {
	enumID := protoDescriptorSymbol(builder, file, enum, name, "protobuf-enum")
	builder.addEdge(parent, enumID, graph.EdgeContains, name, protoRange(file, enum), graph.EvidenceDeclared)
	values := enum.Values()
	for index := range values.Len() {
		value := values.Get(index)
		valueID := protoDescriptorSymbol(builder, file, value, name, "protobuf-enum-value")
		builder.addEdge(enumID, valueID, graph.EdgeContains, name, protoRange(file, value), graph.EvidenceDeclared)
	}
}

func protoRange(file protoreflect.FileDescriptor, descriptor protoreflect.Descriptor) graph.Range {
	location := file.SourceLocations().ByDescriptor(descriptor)
	if location.Path == nil {
		return unknownRange()
	}
	return graph.Range{
		Start: graph.Position{Line: int32(location.StartLine), Column: int32(location.StartColumn), Byte: -1},
		End:   graph.Position{Line: int32(location.EndLine), Column: int32(location.EndColumn), Byte: -1},
	}
}

func findTokenRange(data []byte, token string, after int) graph.Range {
	if after < 0 || after >= len(data) {
		after = 0
	}
	index := strings.Index(string(data[after:]), token)
	if index < 0 {
		return unknownRange()
	}
	start := after + index
	return byteRange(data, start, start+len(token))
}

func sortedStrings(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
