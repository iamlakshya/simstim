package host

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestManifestCloneOwnsAndPreservesOpaqueFuturePolicyCapsule(t *testing.T) {
	raw := json.RawMessage(`{"v":2,"future":{"keep":["original"]},"layers":[]}`)
	original := Manifest{PolicyNext: raw}
	clone := CloneManifest(original)
	if !bytes.Equal(clone.PolicyNext, raw) {
		t.Fatal("manifest clone decoded or discarded an unknown pending policy capsule")
	}
	clone.PolicyNext[0] = '['
	if original.PolicyNext[0] != '{' || raw[0] != '{' {
		t.Fatal("cloned future policy borrowed the actual protected raw capsule")
	}
}
