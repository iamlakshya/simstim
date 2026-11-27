package supply

import (
	"strings"
	"testing"
)

func TestNativeBuildProvenanceNeedsCompleteGvisorDigestPairV1(t *testing.T) {
	item := NewItem(KindGvisor, "runsc", "release-20261102.0-deadbeef", "amd64", []byte("native fixture"), ReleaseFolder("1.2.3")+"/runsc-amd64")
	item.Released = "2026-11-02"
	item.BuildInputsSHA256, item.SBOMSHA256 = strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := item.validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Item){
		func(item *Item) { item.SBOMSHA256 = "" },
		func(item *Item) { item.BuildInputsSHA256 = "unknown" },
		func(item *Item) { item.Kind = KindVMM },
	} {
		bad := item
		change(&bad)
		if err := bad.validate(); err == nil {
			t.Fatal("malformed or unrelated build provenance accepted")
		}
	}
}
