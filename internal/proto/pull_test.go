package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBundleSnapshotCountsEveryExactPairWithoutNormalisingNames(t *testing.T) {
	a := BundleRef{Name: "refs/heads/Topic", OID: strings.Repeat("a", 40)}
	b := BundleRef{Name: "refs/heads/topic", OID: strings.Repeat("b", 40)}
	count, digest := BundleSnapshot([]BundleRef{a, b})
	reversedCount, reversed := BundleSnapshot([]BundleRef{b, a})
	if count != 2 || reversedCount != count || reversed != digest {
		t.Fatal("canonical order changed the complete snapshot")
	}
	for _, changed := range [][]BundleRef{{a}, {a, a, b}, {{Name: strings.ToLower(a.Name), OID: a.OID}, b}, {{Name: a.Name, OID: b.OID}, b}} {
		_, other := BundleSnapshot(changed)
		if other == digest {
			t.Fatal("count, case, duplicate or OID identity was omitted")
		}
	}
}

func TestBundleControlLimitUsesActualEncodedEnvelopeAndID(t *testing.T) {
	args := CtlBundleArgs{}
	payload, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	base, err := json.Marshal(Request{ID: 0, Op: CtlBundlePrepare, Args: payload})
	if err != nil {
		t.Fatal(err)
	}
	args.Cwd = strings.Repeat("x", (64<<10)-1-len(base))
	if !BundleRequestFits(0, args) {
		t.Fatal("an actual64KiB frame was rejected by an arbitrary smaller cap")
	}
	if BundleRequestFits(^uint64(0), args) {
		t.Fatal("actual larger request ID was not counted")
	}
	args.Cwd += "x"
	if BundleRequestFits(0, args) {
		t.Fatal("the existing ctl frame maximum was exceeded")
	}
}
