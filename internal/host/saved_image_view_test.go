package host

import "testing"

func TestSavedImageViewCannotUseGuestOrRetentionClaim(t *testing.T) {
	manifest := &Manifest{Wall: &WallRecord{Level: "microvm", Driver: "chv"}, Run: Runtime{Image: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if got := wallInfoForManifestV1(manifest); got == nil || got.SavedImage != nil {
		t.Fatal("retention-only image digest supplied saved origin")
	}
	manifest.VMNeedV1 = []byte(`{"version":2,"saved_host":{"v":1,"host_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","ref":"fixture_saved","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	if got := wallInfoForManifestV1(manifest); got == nil || got.SavedImage != nil {
		t.Fatal("unknown/unvalidated Need supplied saved origin")
	}
}
