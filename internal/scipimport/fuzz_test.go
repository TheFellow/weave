package scipimport

import (
	"context"
	"testing"
)

func FuzzImport(f *testing.F) {
	root := f.TempDir()
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff})
	importer := Importer{Limits: Limits{MaxIndexBytes: 64 << 10, MaxSourceBytes: 64 << 10, MaxDocuments: 100, MaxFacts: 1_000, MaxStringBytes: 4 << 10, ProtobufDepth: 20}}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = importer.Import(context.Background(), input, Options{RepositoryRoot: root, RepositoryIdentity: "fuzz"})
	})
}
