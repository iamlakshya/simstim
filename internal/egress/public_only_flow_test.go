package egress

import (
	"net/http"
	"net/netip"
	"testing"
)

// Actual Server/control/CONNECT and flowTracker on owned pipes; this is not
// native process/gVisor sandbox traffic or external network acceptance.
func TestPublicOnlyLiveRestrictionClosesExistingPrivateFlow(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	env := startTestServer(t, echoPolicyDialer(t, private))
	policy := publicOnlyPolicy("example.com", 8443, false, private)
	env.call(t, CtlInit, InitArgs{SessionID: "ceiling2", Agent: "shell", Policy: policy})
	stream, reader := openOpaquePathFlow(t, env, "explicit")
	defer stream.Close()
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal("existing ordinary numeric exception failed", err)
	}
	_ = response.Body.Close()
	requireOpaqueEcho(t, stream, reader)
	for index := range policy.Rules {
		if policy.Rules[index].Target == "example.com:8443" {
			policy.Rules[index].PublicOnly = true
		}
	}
	env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: policy})
	requireFlowClosed(t, stream, reader)
}
