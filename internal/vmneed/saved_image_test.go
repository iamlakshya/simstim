package vmneed

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
)

func savedFixtureNeedV1() NeedV1 {
	need := fixtureNeed()
	need.Image.Ref = "fixture_saved"
	need.Image.PlatformDigest = need.Image.Digest
	need.Image.SavedHost = &image.SavedHostV1{Version: 1, HostID: strings.Repeat("c", 32), Ref: need.Image.Ref, Digest: need.Image.Digest}
	return need
}
func TestSavedImageNeedOwnsTypedHostPinAndCanonicalNestedFields(t *testing.T) {
	need := savedFixtureNeedV1()
	cloned := need.Clone()
	cloned.Image.SavedHost.HostID = strings.Repeat("d", 32)
	if need.Image.SavedHost.HostID == cloned.Image.SavedHost.HostID {
		t.Fatal("cloned boot retained mutable saved-host selection")
	}
	data, err := EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeV1(data, "amd64")
	if err != nil || decoded.Image.SavedHost == nil || *decoded.Image.SavedHost != *need.Image.SavedHost {
		t.Fatal("typed host pin changed during canonical Need round trip", err)
	}
	for _, bad := range []string{
		strings.Replace(string(data), `"host_id":`, `"Host_ID":`, 1),
		strings.Replace(string(data), `"saved_host":{"v":1`, `"saved_host":{"v":1,"v":1`, 1),
		strings.Replace(string(data), `"saved_host":{"v":1`, `"saved_host":{"v":1,"path":"/caller/root"`, 1),
	} {
		if got, err := DecodeV1([]byte(bad), "amd64"); err == nil || got != nil {
			t.Fatal("saved-host aliases, duplicate or caller path entered the original Need")
		}
	}
}
