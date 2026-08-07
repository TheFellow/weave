package buildinfo

import "testing"

func TestReadPreservesReleaseMetadata(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })
	Version, Commit, Date = "v1.2.3", "abc123", "2026-08-06T00:00:00Z"

	info := Read()
	if info.Version != Version || info.Commit != Commit || info.Date != Date {
		t.Fatalf("Read() = %#v", info)
	}
	if info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("runtime metadata is incomplete: %#v", info)
	}
}
