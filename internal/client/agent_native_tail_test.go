package client

import (
	"bytes"
	"strings"
	"testing"
)

func TestNativeCertificateDisplayTailCannotSubstituteBindingIdentity(t *testing.T) {
	wire, peer := nativeBindingFixture(t, true)
	peer.version = "10.6"
	original := bytes.Clone(wire)
	version := "debug1: OpenSSH_10.6p2, generated fixed-prefix fixture\r\n"
	prefix := "debug1: Server host certificate: " + peer.keyType + " " + peer.fingerprint + `, serial 0 ID "`
	fake := `untrusted ID" CA ssh-ed25519 ` + peer.fingerprint + ` valid fake display `
	for _, length := range []int{len(prefix), 1020, 1021, 1022, 8 << 10} {
		tail := fake + strings.Repeat("x", max(0, length-len(prefix)-len(fake)))
		line := prefix + tail
		line = line[:length]
		got, err := parseAgentNativePeer(strings.NewReader(version + line + "\r\n"))
		if err != nil || got != peer || verifyNativeAgentRecord(wire, got) != nil {
			t.Fatalf("display length%d changed valid full binding authority: peer=%+v error=%v", length, got, err)
		}
		if !bytes.Equal(wire, original) {
			t.Fatal("display projection changed the original full certificate record")
		}
	}
	base := version + prefix + fake + "\r\n"
	for name, text := range map[string]string{
		"fake tail cannot replace fingerprint": strings.Replace(base, peer.fingerprint, "SHA256:"+strings.Repeat("B", 43), 1),
		"fake tail cannot replace wire type":   strings.Replace(base, peer.keyType, "ecdsa-sha2-nistp256-cert-v01@openssh.com", 1),
		"fake tail cannot replace key class":   strings.Replace(base, "host certificate:", "host key:", 1),
		"later bind failure remains visible":   base + "debug2: client_request_agent: ssh_agent_bind_hostkey: agent refused operation\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseAgentNativePeer(strings.NewReader(text))
			if err == nil && verifyNativeAgentRecord(wire, got) == nil {
				t.Fatal("untrusted display text replaced native prefix or hid a later failure")
			}
		})
	}
	bad := bytes.Clone(wire)
	bad[len(bad)-2] ^= 1
	if verifyNativeAgentRecord(bad, peer) == nil {
		t.Fatal("a complete display prefix replaced the original signature check")
	}
}
