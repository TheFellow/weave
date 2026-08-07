package graph

import (
	"crypto/sha256"
	"encoding/hex"
)

// WorkspacePathID returns the provider-neutral symbol identity for one exact,
// Git-spelled repository path. Content and compiler providers use this anchor
// to join path navigation to language semantics without sharing ownership.
func WorkspacePathID(repositoryIdentity, repositoryPath string) string {
	digest := sha256.Sum256([]byte("weave-workspace/v1\x00file\x00" + repositoryIdentity + "\x00" + repositoryPath))
	return "workspace-file:" + hex.EncodeToString(digest[:])
}
