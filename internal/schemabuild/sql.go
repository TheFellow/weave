package schemabuild

import (
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/bytebase/omni/pg"
	pgast "github.com/bytebase/omni/pg/ast"
)

func parseSQL(builder *factBuilder, files []sourceFile) ([]string, error) {
	var warnings []string
	parsed := make(map[string][]pg.Statement, len(files))
	definedRelations := map[string]bool{}
	for _, file := range files {
		statements, err := pg.Parse(string(file.data))
		if err != nil {
			return warnings, fmt.Errorf("parse PostgreSQL migration %s with Bytebase omni: %w", file.path, err)
		}
		parsed[file.path] = statements
		for _, statement := range statements {
			switch value := statement.AST.(type) {
			case *pgast.CreateStmt:
				definedRelations[sqlRangeName(value.Relation)] = true
			case *pgast.ViewStmt:
				definedRelations[sqlRangeName(value.View)] = true
			case *pgast.CreateSeqStmt:
				definedRelations[sqlRangeName(value.Sequence)] = true
			}
		}
	}
	previousByStream := map[string]string{}
	for _, file := range files {
		migrationRange := byteRange(file.data, 0, 0)
		migrationID := builder.addSymbol("sql/migration", file.path, file.path, "sql-migration", file.path, migrationRange, graph.EvidenceDeclared)
		stream := path.Dir(file.path)
		if previousMigration := previousByStream[stream]; previousMigration != "" {
			// Directory and filename ordering is useful migration topology, but it
			// is not a dependency declared by the SQL source or a selected migration
			// engine. Keep that distinction machine-visible.
			builder.addEdge(migrationID, previousMigration, graph.EdgeDependsOn, file.path, migrationRange, graph.EvidenceInferred)
		}
		previousByStream[stream] = migrationID
		for _, statement := range parsed[file.path] {
			if statement.AST == nil {
				continue
			}
			if !addSQLStatement(builder, file, migrationID, statement, definedRelations) {
				warnings = append(warnings, fmt.Sprintf("%s:%d: PostgreSQL statement %s is parsed but not semantically modeled", file.path, statement.Start.Line, strings.TrimPrefix(reflect.TypeOf(statement.AST).String(), "*ast.")))
			}
		}
	}
	slices.Sort(warnings)
	return warnings, nil
}

func addSQLStatement(builder *factBuilder, file sourceFile, migrationID string, statement pg.Statement, definedRelations map[string]bool) bool {
	statementRange := byteRange(file.data, max(statement.ByteStart, 0), min(statement.ByteEnd, len(file.data)))
	switch value := statement.AST.(type) {
	case *pgast.CreateSchemaStmt:
		id := builder.addSymbol("sql/schema", value.Schemaname, value.Schemaname, "sql-schema", file.path, sqlLocRange(file.data, value.Loc, statementRange), graph.EvidenceDeclared)
		builder.addEdge(migrationID, id, graph.EdgeDefines, file.path, statementRange, graph.EvidenceDeclared)
		return true
	case *pgast.CreateStmt:
		name := sqlRangeName(value.Relation)
		if name == "" {
			return false
		}
		tableRange := sqlLocRange(file.data, value.Relation.Loc, statementRange)
		tableID := builder.addSymbol("sql/relation", name, name, "sql-table", file.path, tableRange, graph.EvidenceDeclared)
		builder.addEdge(migrationID, tableID, graph.EdgeDefines, file.path, tableRange, graph.EvidenceDeclared)
		for _, item := range sqlList(value.TableElts) {
			switch field := item.(type) {
			case *pgast.ColumnDef:
				columnRange := sqlLocRange(file.data, field.Loc, tableRange)
				columnID := builder.addSymbol("sql/column", name+"."+field.Colname, field.Colname, "sql-column", file.path, columnRange, graph.EvidenceDeclared)
				builder.addEdge(tableID, columnID, graph.EdgeContains, file.path, columnRange, graph.EvidenceDeclared)
				addSQLConstraints(builder, file, tableID, field.Constraints, columnRange, definedRelations)
			case *pgast.Constraint:
				addSQLConstraint(builder, file, tableID, field, tableRange, definedRelations)
			}
		}
		for _, inherited := range sqlList(value.InhRelations) {
			if relation, ok := inherited.(*pgast.RangeVar); ok {
				addSQLRelationDependency(builder, file, tableID, relation, graph.EdgeExtends, tableRange, definedRelations)
			}
		}
		for _, constraint := range sqlList(value.Constraints) {
			if item, ok := constraint.(*pgast.Constraint); ok {
				addSQLConstraint(builder, file, tableID, item, tableRange, definedRelations)
			}
		}
		return true
	case *pgast.ViewStmt:
		name := sqlRangeName(value.View)
		rng := sqlLocRange(file.data, value.View.Loc, statementRange)
		viewID := builder.addSymbol("sql/relation", name, name, "sql-view", file.path, rng, graph.EvidenceDeclared)
		builder.addEdge(migrationID, viewID, graph.EdgeDefines, file.path, rng, graph.EvidenceDeclared)
		addSQLQueryDependencies(builder, file, viewID, value.Query, statementRange, definedRelations)
		return true
	case *pgast.IndexStmt:
		name := value.Idxname
		if name == "" && value.Relation != nil {
			name = fmt.Sprintf("%s.<anonymous-index@%d>", sqlRangeName(value.Relation), statement.ByteStart)
		}
		indexID := builder.addSymbol("sql/index", name, name, "sql-index", file.path, sqlLocRange(file.data, value.Loc, statementRange), graph.EvidenceDeclared)
		builder.addEdge(migrationID, indexID, graph.EdgeDefines, file.path, statementRange, graph.EvidenceDeclared)
		addSQLRelationDependency(builder, file, indexID, value.Relation, graph.EdgeDependsOn, statementRange, definedRelations)
		return true
	case *pgast.CreateSeqStmt:
		name := sqlRangeName(value.Sequence)
		sequenceID := builder.addSymbol("sql/relation", name, name, "sql-sequence", file.path, sqlLocRange(file.data, value.Loc, statementRange), graph.EvidenceDeclared)
		builder.addEdge(migrationID, sequenceID, graph.EdgeDefines, file.path, statementRange, graph.EvidenceDeclared)
		return true
	case *pgast.CreateEnumStmt:
		name := sqlNameList(value.TypeName)
		typeID := builder.addSymbol("sql/type", name, name, "sql-enum", file.path, sqlLocRange(file.data, value.Loc, statementRange), graph.EvidenceDeclared)
		builder.addEdge(migrationID, typeID, graph.EdgeDefines, file.path, statementRange, graph.EvidenceDeclared)
		for _, item := range sqlList(value.Vals) {
			if enumValue, ok := item.(*pgast.String); ok {
				valueID := builder.addSymbol("sql/enum-value", name+"."+enumValue.Str, enumValue.Str, "sql-enum-value", file.path, statementRange, graph.EvidenceDeclared)
				builder.addEdge(typeID, valueID, graph.EdgeContains, file.path, statementRange, graph.EvidenceDeclared)
			}
		}
		return true
	case *pgast.CreateFunctionStmt:
		name := sqlNameList(value.Funcname)
		functionID := builder.addSymbol("sql/function", name, name, "sql-function", file.path, sqlLocRange(file.data, value.Loc, statementRange), graph.EvidenceDeclared)
		builder.addEdge(migrationID, functionID, graph.EdgeDefines, file.path, statementRange, graph.EvidenceDeclared)
		if value.SqlBody != nil {
			addSQLQueryDependencies(builder, file, functionID, value.SqlBody, statementRange, definedRelations)
		}
		return true
	case *pgast.AlterTableStmt:
		if value.Relation == nil {
			return false
		}
		target := sqlRelationTarget(builder, sqlRangeName(value.Relation), definedRelations)
		builder.addReference(target, file.path, statementRange, graph.EvidenceDeclared)
		builder.addEdge(migrationID, target, graph.EdgeReferences, file.path, statementRange, graph.EvidenceDeclared)
		return true
	default:
		return false
	}
}

func sqlList(list *pgast.List) []pgast.Node {
	if list == nil {
		return nil
	}
	return list.Items
}

func sqlNameList(list *pgast.List) string {
	var parts []string
	for _, item := range sqlList(list) {
		if value, ok := item.(*pgast.String); ok {
			parts = append(parts, value.Str)
		}
	}
	return strings.Join(parts, ".")
}

func sqlRangeName(value *pgast.RangeVar) string {
	if value == nil {
		return ""
	}
	if value.Schemaname != "" {
		return value.Schemaname + "." + value.Relname
	}
	return value.Relname
}

func sqlLocRange(data []byte, location pgast.Loc, fallback graph.Range) graph.Range {
	if location.Start < 0 || location.End <= location.Start || location.End > len(data) {
		return fallback
	}
	return byteRange(data, location.Start, location.End)
}

func addSQLConstraints(builder *factBuilder, file sourceFile, owner string, constraints *pgast.List, fallback graph.Range, definedRelations map[string]bool) {
	for _, item := range sqlList(constraints) {
		if constraint, ok := item.(*pgast.Constraint); ok {
			addSQLConstraint(builder, file, owner, constraint, fallback, definedRelations)
		}
	}
}

func addSQLConstraint(builder *factBuilder, file sourceFile, owner string, constraint *pgast.Constraint, fallback graph.Range, definedRelations map[string]bool) {
	if constraint == nil || constraint.Pktable == nil {
		return
	}
	addSQLRelationDependency(builder, file, owner, constraint.Pktable, graph.EdgeDependsOn, sqlLocRange(file.data, constraint.Loc, fallback), definedRelations)
}

func addSQLRelationDependency(builder *factBuilder, file sourceFile, owner string, relation *pgast.RangeVar, kind graph.EdgeKind, fallback graph.Range, definedRelations map[string]bool) {
	name := sqlRangeName(relation)
	if name == "" {
		return
	}
	rng := fallback
	if relation != nil {
		rng = sqlLocRange(file.data, relation.Loc, fallback)
	}
	target := sqlRelationTarget(builder, name, definedRelations)
	builder.addReference(target, file.path, rng, graph.EvidenceDeclared)
	builder.addEdge(owner, target, kind, file.path, rng, graph.EvidenceDeclared)
}

func addSQLQueryDependencies(builder *factBuilder, file sourceFile, owner string, node pgast.Node, fallback graph.Range, definedRelations map[string]bool) {
	seen := map[string]bool{}
	pgast.Inspect(node, func(current pgast.Node) bool {
		relation, ok := current.(*pgast.RangeVar)
		if !ok || seen[sqlRangeName(relation)] {
			return true
		}
		seen[sqlRangeName(relation)] = true
		addSQLRelationDependency(builder, file, owner, relation, graph.EdgeDependsOn, fallback, definedRelations)
		return true
	})
}

func sqlRelationTarget(builder *factBuilder, name string, definedRelations map[string]bool) string {
	if definedRelations[name] {
		return builder.localID("sql/relation", name)
	}
	return openID("sql/relation", name)
}
