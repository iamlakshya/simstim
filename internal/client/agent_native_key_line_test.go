package client

import (
	"strings"
	"testing"
)

func TestNativePeerPreservesAuthoritativeCertificatePrefix(t *testing.T) {
	_, peer := nativeBindingFixture(t, true)
	version := "debug1: OpenSSH_10.2p1, generated native grammar fixture\r\n"
	key := "debug1: Server host certificate: " + peer.keyType + " " + peer.fingerprint
	suffix := `, serial 18446744073709551615 ID "fixture" CA ssh-ed25519 ` + peer.fingerprint + " valid forever"
	base := version + key + suffix + "\r\n"
	if got, err := parseAgentNativePeer(strings.NewReader(base)); err != nil || got != peer {
		t.Fatalf("native certificate = %+v, %v", got, err)
	}
	for _, id := range []string{"", `quoted " CA fake ID`, `space and \\ escape`} {
		line := version + key + `, serial 0 ID "` + id + `" CA ssh-ed25519 ` + peer.fingerprint + " valid from 20260101 to 20270101\n"
		if got, err := parseAgentNativePeer(strings.NewReader(line)); err != nil || got != peer {
			t.Fatalf("native display ID %q = %+v, %v", id, got, err)
		}
	}
	for _, display := range []string{
		`fixture" CA ssh-ed25519 ` + peer.fingerprint + " valid ",
		`fixture" CA ssh-ed25519-cert-v01@openssh.com ` + peer.fingerprint,
		`fixture" CA ssh-ed25519 `,
	} {
		text := version + key + `, serial 1 ID "` + display + "\r\n"
		if got, err := parseAgentNativePeer(strings.NewReader(text)); err != nil || got != peer {
			t.Fatalf("untrusted display tail became certificate authority: %+v, %v", got, err)
		}
	}
	for name, text := range map[string]string{
		"plain label":       strings.Replace(base, "host certificate:", "host key:", 1),
		"plain wire type":   strings.Replace(base, peer.keyType, "ssh-ed25519", 1),
		"missing suffix":    version + key + "\n",
		"overflow serial":   strings.Replace(base, "18446744073709551615", "18446744073709551616", 1),
		"signed serial":     strings.Replace(base, "18446744073709551615", "+1", 1),
		"missing ID quote":  strings.Replace(base, `ID "fixture"`, `ID fixture"`, 1),
		"later plain class": base + "debug1: Server host key: ssh-ed25519 " + peer.fingerprint + "\n",
		"later bad suffix":  base + key + ", serial invalid\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAgentNativePeer(strings.NewReader(text)); err == nil {
				t.Fatal("accepted mismatched or missing authoritative native certificate fields")
			}
		})
	}
}

func TestNativePeerPlainRecordRefusesCertificateDisplay(t *testing.T) {
	_, peer := nativeBindingFixture(t, false)
	base := "debug1: OpenSSH_10.2p1, generated fixture\n" +
		"debug1: Server host key: " + peer.keyType + " " + peer.fingerprint
	for _, text := range []string{
		base + " unexpected suffix\n",
		strings.Replace(base, "host key:", "host certificate:", 1) + ", serial 0 ID \"\" CA ssh-ed25519 " + peer.fingerprint + " valid forever\n",
	} {
		if _, err := parseAgentNativePeer(strings.NewReader(text)); err == nil {
			t.Fatal("accepted a plain record with a different native display class")
		}
	}
}
