package api

import (
	"strings"
	"testing"
	"time"
)

func TestNetworkHoldMaximumRequiresClosedRestrictionAndCurrentDescriptorShape(t *testing.T) {
	request := strings.Repeat("a", 32)
	args := NetworkHoldActionArgs{Version: NetworkHoldV1, Session: "root2345", Request: request, Scope: NetworkHoldOnce}
	if ValidNetworkHoldPositiveAction(args) {
		t.Fatal("a legacy frame without revision/maximum became a positive action")
	}
	args.Revision = 3
	for _, maximum := range []NetworkHoldMaximum{NetworkHoldPublicOnly, NetworkHoldExistingPolicy} {
		args.Maximum = maximum
		if !ValidNetworkHoldPositiveAction(args) {
			t.Fatal("a known restrictive frame was rejected")
		}
	}
	args.Maximum = "allow-private"
	if ValidNetworkHoldPositiveAction(args) {
		t.Fatal("an unknown or permissive maximum became a restriction")
	}
	descriptor := NetworkHoldDescriptor{Version: NetworkHoldV1, Request: request, Revision: 3, Class: NetworkHoldPublicTarget, Expires: time.Unix(100, 0).UTC()}
	if !ValidNetworkHoldDescriptor(descriptor, request) {
		t.Fatal("the descriptive public form was rejected")
	}
	// This validates shape only. The old absolute timestamp deliberately
	// proves that descriptor validity is not current permission or a lease.
	descriptor.Class = "unknown"
	if ValidNetworkHoldDescriptor(descriptor, request) {
		t.Fatal("an unknown target class became a known descriptor")
	}
	descriptor.Class, descriptor.Revision = NetworkHoldPrivateTarget, 0
	if ValidNetworkHoldDescriptor(descriptor, request) {
		t.Fatal("a missing permission-relevant generation became a known descriptor")
	}
}
