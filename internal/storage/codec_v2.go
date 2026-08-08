package storage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/TheFellow/weave/internal/graph"
	"github.com/mjl-/bstore"
)

func evidenceCode(value graph.Evidence) (uint8, bool) {
	switch value {
	case graph.EvidenceExact:
		return 1, true
	case graph.EvidenceDeclared:
		return 2, true
	case graph.EvidenceGenerated:
		return 3, true
	case graph.EvidenceInferred:
		return 4, true
	case graph.EvidenceSyntactic:
		return 5, true
	case graph.EvidenceAmbiguous:
		return 6, true
	default:
		return 0, false
	}
}

func evidenceValue(code uint8) (graph.Evidence, bool) {
	switch code {
	case 1:
		return graph.EvidenceExact, true
	case 2:
		return graph.EvidenceDeclared, true
	case 3:
		return graph.EvidenceGenerated, true
	case 4:
		return graph.EvidenceInferred, true
	case 5:
		return graph.EvidenceSyntactic, true
	case 6:
		return graph.EvidenceAmbiguous, true
	default:
		return "", false
	}
}

// Edge codes intentionally follow lexical EdgeKind order. The compound
// adjacency indexes can therefore apply the public canonical order before a
// bounded result is truncated.
func edgeKindCode(value graph.EdgeKind) (uint8, bool) {
	switch value {
	case graph.EdgeCalls:
		return 1, true
	case graph.EdgeContains:
		return 2, true
	case graph.EdgeDependsOn:
		return 3, true
	case graph.EdgeDefines:
		return 4, true
	case graph.EdgeDocuments:
		return 5, true
	case graph.EdgeEmbeds:
		return 6, true
	case graph.EdgeExposes:
		return 7, true
	case graph.EdgeExtends:
		return 8, true
	case graph.EdgeGenerates:
		return 9, true
	case graph.EdgeHandles:
		return 10, true
	case graph.EdgeImplements:
		return 11, true
	case graph.EdgeImports:
		return 12, true
	case graph.EdgeInstantiates:
		return 13, true
	case graph.EdgeLinksTo:
		return 14, true
	case graph.EdgeMemberOf:
		return 15, true
	case graph.EdgeReads:
		return 16, true
	case graph.EdgeReferences:
		return 17, true
	case graph.EdgeResolvesTo:
		return 18, true
	case graph.EdgeTests:
		return 19, true
	case graph.EdgeWrites:
		return 20, true
	default:
		return 0, false
	}
}

func edgeKindValue(code uint8) (graph.EdgeKind, bool) {
	values := [...]graph.EdgeKind{
		"", graph.EdgeCalls, graph.EdgeContains, graph.EdgeDependsOn,
		graph.EdgeDefines, graph.EdgeDocuments, graph.EdgeEmbeds,
		graph.EdgeExposes, graph.EdgeExtends, graph.EdgeGenerates,
		graph.EdgeHandles, graph.EdgeImplements, graph.EdgeImports,
		graph.EdgeInstantiates, graph.EdgeLinksTo, graph.EdgeMemberOf,
		graph.EdgeReads, graph.EdgeReferences, graph.EdgeResolvesTo,
		graph.EdgeTests, graph.EdgeWrites,
	}
	if int(code) >= len(values) || code == 0 {
		return "", false
	}
	return values[code], true
}

type retention struct {
	tx       *bstore.Tx
	interns  map[string]internRecord
	entities map[string]entityRecord
}

func newRetention(tx *bstore.Tx) *retention {
	return &retention{tx: tx, interns: map[string]internRecord{}, entities: map[string]entityRecord{}}
}

func (r *retention) intern(value string) (uint32, error) {
	if value == "" {
		return 0, nil
	}
	if record, ok := r.interns[value]; ok {
		record.Refs++
		r.interns[value] = record
		return record.ID, nil
	}
	record, err := bstore.QueryTx[internRecord](r.tx).FilterEqual("Value", value).Get()
	if err == bstore.ErrAbsent {
		record = internRecord{Value: value, Refs: 1}
		if err := r.tx.Insert(&record); err != nil {
			return 0, err
		}
		r.interns[value] = record
		return record.ID, nil
	}
	if err != nil {
		return 0, err
	}
	record.Refs++
	r.interns[value] = record
	return record.ID, nil
}

func releaseInterns(tx *bstore.Tx, counts map[uint32]uint64) error {
	for id, count := range counts {
		if id == 0 || count == 0 {
			continue
		}
		record, err := bstore.QueryTx[internRecord](tx).FilterID(id).Get()
		if err != nil {
			return err
		}
		if record.Refs < count {
			return fmt.Errorf("intern %d reference count %d is below release %d", id, record.Refs, count)
		}
		if record.Refs == count {
			if err := tx.Delete(&record); err != nil {
				return err
			}
			continue
		}
		record.Refs -= count
		if err := tx.Update(&record); err != nil {
			return err
		}
	}
	return nil
}

func (r *retention) entity(stableID string) (uint64, error) {
	if record, ok := r.entities[stableID]; ok {
		record.Refs++
		r.entities[stableID] = record
		return record.ID, nil
	}
	record, err := bstore.QueryTx[entityRecord](r.tx).FilterEqual("StableID", stableID).Get()
	if err == bstore.ErrAbsent {
		record = entityRecord{StableID: stableID, Refs: 1}
		if err := r.tx.Insert(&record); err != nil {
			return 0, err
		}
		r.entities[stableID] = record
		return record.ID, nil
	}
	if err != nil {
		return 0, err
	}
	record.Refs++
	r.entities[stableID] = record
	return record.ID, nil
}

func (r *retention) flush() error {
	internValues := make([]string, 0, len(r.interns))
	for value := range r.interns {
		internValues = append(internValues, value)
	}
	slices.Sort(internValues)
	for _, value := range internValues {
		record := r.interns[value]
		if err := r.tx.Update(&record); err != nil {
			return err
		}
	}
	entityValues := make([]string, 0, len(r.entities))
	for value := range r.entities {
		entityValues = append(entityValues, value)
	}
	slices.Sort(entityValues)
	for _, value := range entityValues {
		record := r.entities[value]
		if err := r.tx.Update(&record); err != nil {
			return err
		}
	}
	return nil
}

func releaseEntities(tx *bstore.Tx, counts map[uint64]uint64) error {
	for id, count := range counts {
		if count == 0 {
			continue
		}
		record, err := bstore.QueryTx[entityRecord](tx).FilterID(id).Get()
		if err != nil {
			return err
		}
		if record.Refs < count {
			return fmt.Errorf("entity %d reference count %d is below release %d", id, record.Refs, count)
		}
		if record.Refs == count {
			if err := tx.Delete(&record); err != nil {
				return err
			}
			continue
		}
		record.Refs -= count
		if err := tx.Update(&record); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) internID(ctx context.Context, value string) (uint32, bool, error) {
	if value == "" {
		return 0, true, nil
	}
	record, err := bstore.QueryDB[internRecord](ctx, db.db).FilterEqual("Value", value).Get()
	if err == bstore.ErrAbsent {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return record.ID, true, nil
}

func compactNonzero[T ~uint32 | ~uint64](values []T) []T {
	slices.Sort(values)
	values = slices.Compact(values)
	for len(values) > 0 && values[0] == 0 {
		values = values[1:]
	}
	return values
}

func (db *DB) hydrateUnits(ctx context.Context, records []unitRecord) ([]graph.Unit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return hydrateUnits(records, db.internDictionary())
}

func hydrateUnits(records []unitRecord, interns map[uint32]string) ([]graph.Unit, error) {
	ids := make([]uint32, 0, len(records)*3)
	for _, record := range records {
		ids = append(ids, record.Provider, record.ProviderVersion, record.Language)
	}
	if err := requireInterns(interns, ids); err != nil {
		return nil, err
	}
	result := make([]graph.Unit, len(records))
	for i, record := range records {
		result[i] = graph.Unit{ID: record.StableID, Provider: interns[record.Provider], ProviderVersion: interns[record.ProviderVersion], Language: interns[record.Language], Variant: record.Variant, InputFingerprint: record.InputFingerprint, SurfaceFingerprint: record.SurfaceFingerprint, InventoryDigest: record.InventoryDigest}
	}
	return result, nil
}

func (db *DB) hydrateDocuments(ctx context.Context, records []documentRecord) ([]graph.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return hydrateDocuments(records, db.internDictionary())
}

func hydrateDocuments(records []documentRecord, interns map[uint32]string) ([]graph.Document, error) {
	internIDs := make([]uint32, 0, len(records)*3)
	for _, record := range records {
		internIDs = append(internIDs, record.Language, record.Provider, record.ProviderVersion)
	}
	if err := requireInterns(interns, internIDs); err != nil {
		return nil, err
	}
	result := make([]graph.Document, len(records))
	for i, record := range records {
		result[i] = graph.Document{ID: record.StableID, UnitID: record.UnitStable, Path: record.Path, Language: interns[record.Language], ContentHash: record.ContentHash, Provider: interns[record.Provider], ProviderVersion: interns[record.ProviderVersion]}
	}
	return result, nil
}

func (db *DB) hydrateSymbols(ctx context.Context, records []symbolRecord) ([]graph.Symbol, error) {
	var result []graph.Symbol
	err := db.db.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		result, err = hydrateSymbols(tx, records, db.internDictionary())
		return err
	})
	return result, err
}

func hydrateSymbols(tx *bstore.Tx, records []symbolRecord, interns map[uint32]string) ([]graph.Symbol, error) {
	if len(records) == 0 {
		return []graph.Symbol{}, nil
	}
	ids := make([]uint64, len(records))
	kindIDs := make([]uint32, len(records))
	for i, record := range records {
		ids[i], kindIDs[i] = record.ID, record.Kind
	}
	details, err := bstore.QueryTx[symbolDetailRecord](tx).FilterIDs(compactNonzero(append([]uint64(nil), ids...))).List()
	if err != nil {
		return nil, err
	}
	if len(details) != len(records) {
		return nil, logicalCorrupt("missing symbol detail record")
	}
	detailByID := make(map[uint64]symbolDetailRecord, len(details))
	internIDs := append([]uint32(nil), kindIDs...)
	for _, detail := range details {
		detailByID[detail.ID] = detail
		internIDs = append(internIDs, detail.Provider)
	}
	if err := requireInterns(interns, internIDs); err != nil {
		return nil, err
	}
	result := make([]graph.Symbol, len(records))
	for i, record := range records {
		detail := detailByID[record.ID]
		evidence, ok := evidenceValue(detail.Evidence)
		if !ok {
			return nil, logicalCorrupt("invalid symbol evidence code")
		}
		var searchTerms []string
		if record.SearchTerms != "" {
			searchTerms = strings.Split(record.SearchTerms, "\x00")
		}
		result[i] = graph.Symbol{ID: record.StableID, UnitID: record.UnitStable, StableName: record.StableName, DisplayName: record.DisplayName, NormalizedName: record.NormalizedName, SearchTerms: searchTerms, Kind: interns[record.Kind], DocumentID: record.DocumentStable, Definition: detail.Definition, Provider: interns[detail.Provider], Evidence: evidence}
	}
	return result, nil
}

func (db *DB) hydrateOccurrences(ctx context.Context, records []occurrenceRecord) ([]graph.Occurrence, error) {
	var result []graph.Occurrence
	err := db.db.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		result, err = hydrateOccurrences(tx, records, db.internDictionary())
		return err
	})
	return result, err
}

func hydrateOccurrences(tx *bstore.Tx, records []occurrenceRecord, interns map[uint32]string) ([]graph.Occurrence, error) {
	if len(records) == 0 {
		return []graph.Occurrence{}, nil
	}
	ids := make([]uint64, len(records))
	roleIDs := make([]uint32, len(records))
	for i, record := range records {
		ids[i], roleIDs[i] = record.ID, record.Role
	}
	details, err := bstore.QueryTx[occurrenceDetailRecord](tx).FilterIDs(compactNonzero(append([]uint64(nil), ids...))).List()
	if err != nil {
		return nil, err
	}
	if len(details) != len(records) {
		return nil, logicalCorrupt("missing occurrence detail record")
	}
	detailByID := make(map[uint64]occurrenceDetailRecord, len(details))
	internIDs := append([]uint32(nil), roleIDs...)
	for _, detail := range details {
		detailByID[detail.ID] = detail
		internIDs = append(internIDs, detail.Provider)
	}
	if err := requireInterns(interns, internIDs); err != nil {
		return nil, err
	}
	result := make([]graph.Occurrence, len(records))
	for i, record := range records {
		detail := detailByID[record.ID]
		evidence, ok := evidenceValue(detail.Evidence)
		if !ok {
			return nil, logicalCorrupt("invalid occurrence evidence code")
		}
		result[i] = graph.Occurrence{ID: record.StableID, UnitID: record.UnitStable, SymbolID: record.SymbolStable, DocumentID: record.DocumentStable, Role: interns[record.Role], Range: detail.Range, Provider: interns[detail.Provider], Evidence: evidence}
	}
	return result, nil
}

func (db *DB) hydrateEdges(ctx context.Context, records []edgeRecord) ([]graph.Edge, error) {
	var result []graph.Edge
	err := db.db.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		result, err = hydrateEdges(tx, records, db.internDictionary())
		return err
	})
	return result, err
}

func hydrateEdges(tx *bstore.Tx, records []edgeRecord, interns map[uint32]string) ([]graph.Edge, error) {
	if len(records) == 0 {
		return []graph.Edge{}, nil
	}
	ids := make([]uint64, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	details, err := bstore.QueryTx[edgeDetailRecord](tx).FilterIDs(compactNonzero(append([]uint64(nil), ids...))).List()
	if err != nil {
		return nil, err
	}
	if len(details) != len(records) {
		return nil, logicalCorrupt("missing edge detail record")
	}
	detailByID := make(map[uint64]edgeDetailRecord, len(details))
	internIDs := make([]uint32, 0, len(details))
	for _, detail := range details {
		detailByID[detail.ID] = detail
		internIDs = append(internIDs, detail.Provider)
	}
	if err := requireInterns(interns, internIDs); err != nil {
		return nil, err
	}
	result := make([]graph.Edge, len(records))
	for i, record := range records {
		detail := detailByID[record.ID]
		kind, ok := edgeKindValue(record.Kind)
		if !ok {
			return nil, logicalCorrupt("invalid edge kind code")
		}
		evidence, ok := evidenceValue(detail.Evidence)
		if !ok {
			return nil, logicalCorrupt("invalid edge evidence code")
		}
		result[i] = graph.Edge{ID: record.StableID, UnitID: record.UnitStable, From: record.FromStable, To: record.ToStable, Kind: kind, Evidence: evidence, DocumentID: record.DocumentStable, Range: detail.Range, Provider: interns[detail.Provider]}
	}
	return result, nil
}

func requireInterns(dictionary map[uint32]string, ids []uint32) error {
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := dictionary[id]; !ok {
			return logicalCorrupt("missing intern record")
		}
	}
	return nil
}

func logicalCorrupt(detail string) error {
	return fmt.Errorf("%w: %s; delete and rebuild this derived index", ErrCorrupt, detail)
}
