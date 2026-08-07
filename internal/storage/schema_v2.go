package storage

import "github.com/TheFellow/weave/internal/graph"

// StorageSchemaVersion is independent from graph.SchemaVersion: the latter is
// the stable normalized/export model, while this number describes the private,
// disposable bstore layout.
const StorageSchemaVersion uint32 = 2

type internRecord struct {
	ID    uint32 `bstore:"typename WeaveV2Intern"`
	Value string `bstore:"unique"`
	Refs  uint64
}

// entityRecord maps stable external symbol identities to compact internal
// identities. It also represents external endpoints which do not have a local
// symbolRecord.
type entityRecord struct {
	ID       uint64 `bstore:"typename WeaveV2Entity"`
	StableID string `bstore:"unique"`
	Refs     uint64
}

type unitRecord struct {
	ID                 uint64 `bstore:"typename WeaveV2Unit"`
	StableID           string `bstore:"unique"`
	Provider           uint32
	ProviderVersion    uint32
	Language           uint32
	Variant            string
	InputFingerprint   string
	SurfaceFingerprint string
	InventoryDigest    string
}

type documentRecord struct {
	ID              uint64 `bstore:"typename WeaveV2Document"`
	StableID        string `bstore:"unique"`
	Unit            uint64 `bstore:"index,index Unit+Path"`
	UnitStable      string
	Path            string
	Language        uint32
	ContentHash     string
	Provider        uint32
	ProviderVersion uint32
}

// symbolRecord is the search/traversal projection. Definition range, provider
// and evidence live in symbolDetailRecord and are fetched only when a complete
// public fact is requested.
type symbolRecord struct {
	ID             uint64 `bstore:"typename WeaveV2Symbol,noauto"`
	StableID       string `bstore:"unique"`
	Unit           uint64 `bstore:"index"`
	UnitStable     string
	StableName     string `bstore:"index,index StableName+StableID"`
	DisplayName    string
	NormalizedName string `bstore:"index,index NormalizedName+StableID"`
	Kind           uint32 `bstore:"index"`
	Document       uint64 `bstore:"index"`
	DocumentStable string
}

type symbolDetailRecord struct {
	ID         uint64 `bstore:"typename WeaveV2SymbolDetail,noauto"`
	Definition graph.Range
	Provider   uint32
	Evidence   uint8
}

type occurrenceRecord struct {
	ID             uint64 `bstore:"typename WeaveV2Occurrence"`
	StableID       string `bstore:"unique"`
	Unit           uint64 `bstore:"index"`
	UnitStable     string
	Symbol         uint64 `bstore:"index Symbol+DocumentStable+StableID"`
	SymbolStable   string
	Document       uint64 `bstore:"index"`
	DocumentStable string
	Role           uint32 `bstore:"index"`
}

type occurrenceDetailRecord struct {
	ID       uint64 `bstore:"typename WeaveV2OccurrenceDetail,noauto"`
	Range    graph.Range
	Provider uint32
	Evidence uint8
}

// Stable endpoint and edge strings are retained as ordering keys so bounded
// results stay byte-for-byte equivalent to graph.CompareEdges. Numeric From/To
// are the lookup keys used on the hot adjacency path.
type edgeRecord struct {
	ID             uint64 `bstore:"typename WeaveV2Edge"`
	StableID       string `bstore:"unique"`
	Unit           uint64 `bstore:"index"`
	UnitStable     string
	From           uint64 `bstore:"index From+Kind+ToStable+StableID"`
	To             uint64 `bstore:"index To+Kind+FromStable+StableID"`
	Kind           uint8
	FromStable     string
	ToStable       string
	DocumentStable string
}

type edgeDetailRecord struct {
	ID       uint64 `bstore:"typename WeaveV2EdgeDetail,noauto"`
	Evidence uint8
	Document uint64
	Range    graph.Range
	Provider uint32
}

type tokenRecord struct {
	ID           uint64 `bstore:"typename WeaveV2SymbolToken"`
	Unit         uint64 `bstore:"index"`
	Token        string `bstore:"index Token+SymbolStable"`
	Symbol       uint64 `bstore:"index"`
	SymbolStable string
}

var recordTypes = []any{
	generationRecord{}, internRecord{}, entityRecord{}, unitRecord{}, documentRecord{},
	symbolRecord{}, symbolDetailRecord{}, occurrenceRecord{}, occurrenceDetailRecord{},
	edgeRecord{}, edgeDetailRecord{}, tokenRecord{},
}
