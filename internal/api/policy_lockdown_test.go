package api

import (
	"strings"
	"testing"
)

func TestLockdownCASBindsCurrentSessionAndRestrictionWithoutChangingLaptopHash(t *testing.T) {
	layers := []Layer{{Kind: LayerAll}, {Kind: LayerHost, Name: "box"}, {Kind: LayerSession, Rules: []Rule{{Allow: true, Target: "session.example", GrantID: "original"}}}}
	before, err := HashPolicyLayerVectorV1(layers)
	if err != nil {
		t.Fatal(err)
	}
	laptop, _ := HashPolicyLaptopLayersV1(layers)
	layers[2].Rules[0].GrantID = "next-instance"
	after, _ := HashPolicyLayerVectorV1(layers)
	if before == after {
		t.Fatal("session mutation escaped lockdown CAS")
	}
	layers = append(layers, Layer{Kind: LayerLockdown})
	locked, _ := HashPolicyLayerVectorV1(layers)
	unchanged, _ := HashPolicyLaptopLayersV1(layers)
	if locked == after || laptop != unchanged {
		t.Fatal("restriction did not bind CAS or changed the unrelated laptop hash")
	}
	if (PolicyLockdownArgsV1{Version: 1, On: false, Session: "root2345", PreviousHash: locked}).Validate() != nil {
		t.Fatal("valid current-session operation refused")
	}
	if (PolicyLockdownArgsV1{Version: 1, On: false, Session: "root2345"}).Validate() == nil || (PolicyLockdownArgsV1{Version: 1, On: true, PreviousHash: strings.Repeat("a", 64)}).Validate() == nil {
		t.Fatal("global and session scopes aliased")
	}
}
