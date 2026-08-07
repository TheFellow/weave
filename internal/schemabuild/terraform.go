package schemabuild

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

func parseTerraform(builder *factBuilder, files []sourceFile) ([]string, error) {
	moduleDirs := map[string]bool{}
	bodies := map[string]*hclsyntax.Body{}
	definedAddresses := map[string]bool{}
	for _, file := range files {
		moduleDirs[path.Dir(file.path)] = true
		parsed, diagnostics := hclsyntax.ParseConfig(file.data, file.path, hcl.Pos{Line: 1, Column: 1})
		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("parse %s with HashiCorp HCL: %s", file.path, diagnostics.Error())
		}
		body, ok := parsed.Body.(*hclsyntax.Body)
		if !ok {
			return nil, fmt.Errorf("parse %s: unexpected HCL body type", file.path)
		}
		bodies[file.path] = body
		collectTerraformAddresses(path.Dir(file.path), body, definedAddresses)
	}
	for _, directory := range sortedStrings(moduleDirs) {
		display := directory
		if display == "." {
			display = "root module"
		}
		builder.addSymbol("terraform/module-root", directory, display, "terraform-module", "", unknownRange(), graph.EvidenceDeclared)
	}
	for _, file := range files {
		body := bodies[file.path]
		moduleDir := path.Dir(file.path)
		moduleID := builder.localID("terraform/module-root", moduleDir)
		for _, block := range body.Blocks {
			addTerraformBlock(builder, file, moduleDir, moduleID, moduleDirs, definedAddresses, block)
		}
	}
	return nil, nil
}

func collectTerraformAddresses(moduleDir string, body *hclsyntax.Body, defined map[string]bool) {
	for _, block := range body.Blocks {
		var address string
		switch block.Type {
		case "resource":
			if len(block.Labels) >= 2 {
				address = "resource." + block.Labels[0] + "." + block.Labels[1]
			}
		case "data":
			if len(block.Labels) >= 2 {
				address = "data." + block.Labels[0] + "." + block.Labels[1]
			}
		case "variable", "output", "module", "provider":
			if len(block.Labels) >= 1 {
				address = block.Type + "." + block.Labels[0]
			}
		case "locals":
			for name := range block.Body.Attributes {
				defined[moduleDir+":local."+name] = true
			}
		}
		if address != "" {
			defined[moduleDir+":"+address] = true
		}
	}
}

func addTerraformBlock(builder *factBuilder, file sourceFile, moduleDir, moduleID string, moduleDirs, definedAddresses map[string]bool, block *hclsyntax.Block) {
	if block == nil {
		return
	}
	rng := hclRange(block.TypeRange, file.data)
	owner := moduleID
	switch block.Type {
	case "resource":
		if len(block.Labels) >= 2 {
			address := "resource." + block.Labels[0] + "." + block.Labels[1]
			owner = builder.addSymbol("terraform/address", moduleDir+":"+address, block.Labels[0]+"."+block.Labels[1], "terraform-resource", file.path, hclRange(block.LabelRanges[1], file.data), graph.EvidenceDeclared)
			builder.addEdge(moduleID, owner, graph.EdgeContains, file.path, rng, graph.EvidenceDeclared)
		}
	case "data":
		if len(block.Labels) >= 2 {
			address := "data." + block.Labels[0] + "." + block.Labels[1]
			owner = builder.addSymbol("terraform/address", moduleDir+":"+address, block.Labels[0]+"."+block.Labels[1], "terraform-data-source", file.path, hclRange(block.LabelRanges[1], file.data), graph.EvidenceDeclared)
			builder.addEdge(moduleID, owner, graph.EdgeContains, file.path, rng, graph.EvidenceDeclared)
		}
	case "variable", "output", "module", "provider":
		if len(block.Labels) >= 1 {
			address := block.Type + "." + block.Labels[0]
			owner = builder.addSymbol("terraform/address", moduleDir+":"+address, block.Labels[0], "terraform-"+block.Type, file.path, hclRange(block.LabelRanges[0], file.data), graph.EvidenceDeclared)
			builder.addEdge(moduleID, owner, graph.EdgeContains, file.path, rng, graph.EvidenceDeclared)
		}
	case "locals":
		for name, attribute := range block.Body.Attributes {
			localID := builder.addSymbol("terraform/address", moduleDir+":local."+name, name, "terraform-local", file.path, hclRange(attribute.NameRange, file.data), graph.EvidenceDeclared)
			builder.addEdge(moduleID, localID, graph.EdgeContains, file.path, hclRange(attribute.NameRange, file.data), graph.EvidenceDeclared)
			addTerraformExpressionEdges(builder, file, moduleDir, localID, attribute.Expr, false, definedAddresses)
		}
	case "terraform":
		owner = moduleID
	}

	attributeNames := make([]string, 0, len(block.Body.Attributes))
	for name := range block.Body.Attributes {
		attributeNames = append(attributeNames, name)
	}
	slices.Sort(attributeNames)
	for _, name := range attributeNames {
		attribute := block.Body.Attributes[name]
		depends := name == "depends_on"
		addTerraformExpressionEdges(builder, file, moduleDir, owner, attribute.Expr, depends, definedAddresses)
		if block.Type == "module" && name == "source" {
			addTerraformModuleSource(builder, file, moduleDir, owner, moduleDirs, attribute)
		}
		if block.Type == "terraform" && name == "required_providers" {
			addTerraformProviderRequirements(builder, file, moduleID, attribute)
		}
	}
	if block.Type == "terraform" {
		for _, nested := range block.Body.Blocks {
			if nested.Type != "required_providers" {
				continue
			}
			providerNames := make([]string, 0, len(nested.Body.Attributes))
			for name := range nested.Body.Attributes {
				providerNames = append(providerNames, name)
			}
			slices.Sort(providerNames)
			for _, name := range providerNames {
				addTerraformProviderRequirement(builder, file, moduleID, name, nested.Body.Attributes[name])
			}
		}
	}
}

func addTerraformExpressionEdges(builder *factBuilder, file sourceFile, moduleDir, owner string, expression hclsyntax.Expression, depends bool, definedAddresses map[string]bool) {
	if expression == nil {
		return
	}
	for _, traversal := range expression.Variables() {
		address, ok := terraformTraversalAddress(traversal)
		if !ok {
			continue
		}
		rng := hclRange(traversal.SourceRange(), file.data)
		stable := moduleDir + ":" + address
		target := openID("terraform/address", stable)
		if definedAddresses[stable] {
			target = builder.localID("terraform/address", stable)
		}
		builder.addReference(target, file.path, rng, graph.EvidenceDeclared)
		kind := graph.EdgeReferences
		if depends {
			kind = graph.EdgeDependsOn
		}
		builder.addEdge(owner, target, kind, file.path, rng, graph.EvidenceDeclared)
	}
}

func terraformTraversalAddress(traversal hcl.Traversal) (string, bool) {
	var parts []string
	for _, step := range traversal {
		switch value := step.(type) {
		case hcl.TraverseRoot:
			parts = append(parts, value.Name)
		case hcl.TraverseAttr:
			parts = append(parts, value.Name)
		default:
			return "", false
		}
	}
	if len(parts) < 2 {
		return "", false
	}
	switch parts[0] {
	case "var", "local", "module":
		return strings.Join(parts[:2], "."), true
	case "data":
		if len(parts) >= 3 {
			return strings.Join(parts[:3], "."), true
		}
	case "each", "count", "path", "terraform", "self":
		return "", false
	default:
		return "resource." + strings.Join(parts[:2], "."), true
	}
	return "", false
}

func addTerraformModuleSource(builder *factBuilder, file sourceFile, moduleDir, owner string, moduleDirs map[string]bool, attribute *hclsyntax.Attribute) {
	value, diagnostics := attribute.Expr.Value(nil)
	if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || value.Type() != cty.String {
		return
	}
	source := value.AsString()
	rng := hclRange(attribute.Expr.Range(), file.data)
	target := openID("terraform/module-source", source)
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		candidate := path.Clean(path.Join(moduleDir, source))
		if (candidate == "." || validPath(candidate)) && !strings.HasPrefix(candidate, "../") && moduleDirs[candidate] {
			target = builder.localID("terraform/module-root", candidate)
		}
	}
	builder.addReference(target, file.path, rng, graph.EvidenceDeclared)
	builder.addEdge(owner, target, graph.EdgeDependsOn, file.path, rng, graph.EvidenceDeclared)
}

func addTerraformProviderRequirements(builder *factBuilder, file sourceFile, moduleID string, attribute *hclsyntax.Attribute) {
	object, ok := attribute.Expr.(*hclsyntax.ObjectConsExpr)
	if !ok {
		return
	}
	for _, item := range object.Items {
		value, diagnostics := item.KeyExpr.Value(nil)
		if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || value.Type() != cty.String {
			continue
		}
		name := value.AsString()
		addTerraformProviderRequirementExpression(builder, file, moduleID, name, item.ValueExpr, item.KeyExpr.Range())
	}
}

func addTerraformProviderRequirement(builder *factBuilder, file sourceFile, moduleID, name string, attribute *hclsyntax.Attribute) {
	addTerraformProviderRequirementExpression(builder, file, moduleID, name, attribute.Expr, attribute.NameRange)
}

func addTerraformProviderRequirementExpression(builder *factBuilder, file sourceFile, moduleID, name string, expression hclsyntax.Expression, nameRange hcl.Range) {
	source := name
	if object, ok := expression.(*hclsyntax.ObjectConsExpr); ok {
		for _, item := range object.Items {
			key, keyDiagnostics := item.KeyExpr.Value(nil)
			value, valueDiagnostics := item.ValueExpr.Value(nil)
			if keyDiagnostics.HasErrors() || valueDiagnostics.HasErrors() || key.Type() != cty.String || value.Type() != cty.String || key.IsNull() || value.IsNull() {
				continue
			}
			if key.AsString() == "source" {
				source = value.AsString()
				break
			}
		}
	}
	rng := hclRange(nameRange, file.data)
	target := openID("terraform/provider", source)
	builder.addReference(target, file.path, rng, graph.EvidenceDeclared)
	builder.addEdge(moduleID, target, graph.EdgeDependsOn, file.path, rng, graph.EvidenceDeclared)
}

func hclRange(value hcl.Range, data []byte) graph.Range {
	start, end := value.Start.Byte, value.End.Byte
	if start < 0 || end < start || end > len(data) {
		return unknownRange()
	}
	return byteRange(data, start, end)
}
