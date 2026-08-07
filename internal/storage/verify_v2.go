package storage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mjl-/bstore"
)

// verifyStorage checks the v2 indirection/cold-record invariants before Export
// attempts to hydrate them. This keeps verify useful even when logical damage
// makes a complete public fact impossible to reconstruct.
func (db *DB) verifyStorage(ctx context.Context) ([]Issue, error) {
	units, err := bstore.QueryDB[unitRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify units", err)
	}
	documents, err := bstore.QueryDB[documentRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify documents", err)
	}
	symbols, err := bstore.QueryDB[symbolRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify symbols", err)
	}
	occurrences, err := bstore.QueryDB[occurrenceRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify occurrences", err)
	}
	edges, err := bstore.QueryDB[edgeRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify edges", err)
	}
	symbolDetails, err := bstore.QueryDB[symbolDetailRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify symbol details", err)
	}
	occurrenceDetails, err := bstore.QueryDB[occurrenceDetailRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify occurrence details", err)
	}
	edgeDetails, err := bstore.QueryDB[edgeDetailRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify edge details", err)
	}
	entities, err := bstore.QueryDB[entityRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify identities", err)
	}
	interns, err := bstore.QueryDB[internRecord](ctx, db.db).List()
	if err != nil {
		return nil, classify("verify interns", err)
	}

	unitByID := make(map[uint64]unitRecord, len(units))
	documentByID := make(map[uint64]documentRecord, len(documents))
	entityByID := make(map[uint64]entityRecord, len(entities))
	symbolByID := make(map[uint64]symbolRecord, len(symbols))
	occurrenceByID := make(map[uint64]occurrenceRecord, len(occurrences))
	edgeByID := make(map[uint64]edgeRecord, len(edges))
	symbolDetailByID := make(map[uint64]symbolDetailRecord, len(symbolDetails))
	occurrenceDetailByID := make(map[uint64]occurrenceDetailRecord, len(occurrenceDetails))
	edgeDetailByID := make(map[uint64]edgeDetailRecord, len(edgeDetails))
	for _, record := range units {
		unitByID[record.ID] = record
	}
	for _, record := range documents {
		documentByID[record.ID] = record
	}
	for _, record := range entities {
		entityByID[record.ID] = record
	}
	for _, record := range symbols {
		symbolByID[record.ID] = record
	}
	for _, record := range occurrences {
		occurrenceByID[record.ID] = record
	}
	for _, record := range edges {
		edgeByID[record.ID] = record
	}
	for _, record := range symbolDetails {
		symbolDetailByID[record.ID] = record
	}
	for _, record := range occurrenceDetails {
		occurrenceDetailByID[record.ID] = record
	}
	for _, record := range edgeDetails {
		edgeDetailByID[record.ID] = record
	}

	var issues []Issue
	errorIssue := func(kind, record, detail, document string) {
		issues = append(issues, Issue{Severity: IssueError, Kind: kind, Record: record, Detail: detail, Document: document})
	}
	for _, record := range documents {
		if unit, ok := unitByID[record.Unit]; !ok {
			errorIssue("orphan-document", record.StableID, fmt.Sprintf("internal unit %d is absent", record.Unit), record.Path)
		} else if unit.StableID != record.UnitStable {
			errorIssue("invalid-document-unit-identity", record.StableID, "numeric and stable unit identities disagree", record.Path)
		}
	}
	for _, record := range symbols {
		stable := fmt.Sprintf("internal:%d", record.ID)
		if entity, ok := entityByID[record.ID]; ok {
			stable = entity.StableID
			if entity.StableID != record.StableID {
				errorIssue("invalid-symbol-identity", record.StableID, "entity and hot stable identities disagree", "")
			}
		} else {
			errorIssue("missing-symbol-identity", stable, "stable identity is absent", "")
		}
		if unit, ok := unitByID[record.Unit]; !ok {
			errorIssue("orphan-symbol", stable, fmt.Sprintf("internal unit %d is absent", record.Unit), "")
		} else if unit.StableID != record.UnitStable {
			errorIssue("invalid-symbol-unit-identity", stable, "numeric and stable unit identities disagree", "")
		}
		if record.Document != 0 {
			if document, ok := documentByID[record.Document]; !ok {
				errorIssue("invalid-symbol-document", stable, fmt.Sprintf("internal document %d is absent", record.Document), "")
			} else if document.StableID != record.DocumentStable {
				errorIssue("invalid-symbol-document-identity", stable, "numeric and stable document identities disagree", "")
			}
		} else if record.DocumentStable != "" {
			errorIssue("invalid-symbol-document-identity", stable, "stable document is set without numeric document", "")
		}
		if _, ok := symbolDetailByID[record.ID]; !ok {
			errorIssue("missing-symbol-detail", stable, "cold detail is absent", "")
		}
	}
	for _, record := range occurrences {
		if unit, ok := unitByID[record.Unit]; !ok {
			errorIssue("orphan-occurrence", record.StableID, fmt.Sprintf("internal unit %d is absent", record.Unit), record.DocumentStable)
		} else if unit.StableID != record.UnitStable {
			errorIssue("invalid-occurrence-unit-identity", record.StableID, "numeric and stable unit identities disagree", record.DocumentStable)
		}
		if entity, ok := entityByID[record.Symbol]; !ok {
			errorIssue("missing-occurrence-identity", record.StableID, fmt.Sprintf("internal symbol identity %d is absent", record.Symbol), record.DocumentStable)
		} else if entity.StableID != record.SymbolStable {
			errorIssue("invalid-occurrence-symbol-identity", record.StableID, "numeric and stable symbol identities disagree", record.DocumentStable)
		}
		if document, ok := documentByID[record.Document]; !ok {
			errorIssue("invalid-occurrence-document", record.StableID, fmt.Sprintf("internal document %d is absent", record.Document), record.DocumentStable)
		} else if document.StableID != record.DocumentStable {
			errorIssue("invalid-occurrence-document-identity", record.StableID, "numeric and stable document identities disagree", record.DocumentStable)
		}
		if _, ok := occurrenceDetailByID[record.ID]; !ok {
			errorIssue("missing-occurrence-detail", record.StableID, "cold detail is absent", record.DocumentStable)
		}
	}
	for _, record := range edges {
		if unit, ok := unitByID[record.Unit]; !ok {
			errorIssue("orphan-edge", record.StableID, fmt.Sprintf("internal unit %d is absent", record.Unit), "")
		} else if unit.StableID != record.UnitStable {
			errorIssue("invalid-edge-unit-identity", record.StableID, "numeric and stable unit identities disagree", record.DocumentStable)
		}
		if entity, ok := entityByID[record.From]; !ok {
			errorIssue("missing-edge-identity", record.StableID, fmt.Sprintf("from identity %d is absent", record.From), "")
		} else if entity.StableID != record.FromStable {
			errorIssue("invalid-edge-from-identity", record.StableID, "numeric and stable from identities disagree", record.DocumentStable)
		}
		if entity, ok := entityByID[record.To]; !ok {
			errorIssue("missing-edge-identity", record.StableID, fmt.Sprintf("to identity %d is absent", record.To), "")
		} else if entity.StableID != record.ToStable {
			errorIssue("invalid-edge-to-identity", record.StableID, "numeric and stable to identities disagree", record.DocumentStable)
		}
		if _, ok := edgeDetailByID[record.ID]; !ok {
			errorIssue("missing-edge-detail", record.StableID, "cold detail is absent", "")
		}
		if detail, ok := edgeDetailByID[record.ID]; ok {
			if detail.Document == 0 && record.DocumentStable != "" {
				errorIssue("invalid-edge-document-identity", record.StableID, "stable document is set without numeric document", record.DocumentStable)
			} else if detail.Document != 0 {
				if document, ok := documentByID[detail.Document]; !ok || document.StableID != record.DocumentStable {
					errorIssue("invalid-edge-document-identity", record.StableID, "numeric and stable document identities disagree", record.DocumentStable)
				}
			}
		}
	}
	for _, detail := range symbolDetails {
		if _, ok := symbolByID[detail.ID]; !ok {
			errorIssue("orphan-symbol-detail", fmt.Sprint(detail.ID), "symbol is absent", "")
		}
	}
	for _, detail := range occurrenceDetails {
		if _, ok := occurrenceByID[detail.ID]; !ok {
			errorIssue("orphan-occurrence-detail", fmt.Sprint(detail.ID), "occurrence is absent", "")
		}
	}
	for _, detail := range edgeDetails {
		if _, ok := edgeByID[detail.ID]; !ok {
			errorIssue("orphan-edge-detail", fmt.Sprint(detail.ID), "edge is absent", "")
		}
	}

	actualIntern := map[uint32]uint64{}
	addIntern := func(values ...uint32) {
		for _, value := range values {
			if value != 0 {
				actualIntern[value]++
			}
		}
	}
	for _, record := range units {
		addIntern(record.Provider, record.ProviderVersion, record.Language)
	}
	for _, record := range documents {
		addIntern(record.Language, record.Provider, record.ProviderVersion)
	}
	for _, record := range symbols {
		addIntern(record.Kind)
	}
	for _, record := range symbolDetails {
		addIntern(record.Provider)
	}
	for _, record := range occurrences {
		addIntern(record.Role)
	}
	for _, record := range occurrenceDetails {
		addIntern(record.Provider)
	}
	for _, record := range edgeDetails {
		addIntern(record.Provider)
	}
	for _, record := range interns {
		if actualIntern[record.ID] != record.Refs {
			errorIssue("invalid-intern-refcount", record.Value, fmt.Sprintf("stored %d, actual %d", record.Refs, actualIntern[record.ID]), "")
		}
		delete(actualIntern, record.ID)
	}
	for id, count := range actualIntern {
		errorIssue("missing-intern", fmt.Sprint(id), fmt.Sprintf("referenced %d times", count), "")
	}

	actualEntities := map[uint64]uint64{}
	for _, record := range symbols {
		actualEntities[record.ID]++
	}
	for _, record := range occurrences {
		actualEntities[record.Symbol]++
	}
	for _, record := range edges {
		actualEntities[record.From]++
		actualEntities[record.To]++
	}
	for _, record := range entities {
		if actualEntities[record.ID] != record.Refs {
			errorIssue("invalid-identity-refcount", record.StableID, fmt.Sprintf("stored %d, actual %d", record.Refs, actualEntities[record.ID]), "")
		}
		delete(actualEntities, record.ID)
	}
	for id, count := range actualEntities {
		errorIssue("missing-identity", fmt.Sprint(id), fmt.Sprintf("referenced %d times", count), "")
	}

	slices.SortFunc(issues, func(a, b Issue) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Record, b.Record)
	})
	return issues, nil
}
