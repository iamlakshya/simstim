package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func nativeBindingFixture(t *testing.T, certificate bool) ([]byte, agentNativePeer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := signer.PublicKey()
	if certificate {
		_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ca, err := ssh.NewSignerFromKey(caPrivate)
		if err != nil {
			t.Fatal(err)
		}
		cert := &ssh.Certificate{Key: key, CertType: ssh.HostCert, ValidPrincipals: []string{"example.invalid"}, ValidBefore: ssh.CertTimeInfinity}
		if err := cert.SignCert(rand.Reader, ca); err != nil {
			t.Fatal(err)
		}
		key = cert
	}
	session := []byte("generated native-session fixture")
	signature, err := signer.Sign(rand.Reader, session)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	body.WriteByte(agentFWExtension)
	for _, value := range [][]byte{[]byte(agentFWSessionBind), key.Marshal(), session, ssh.Marshal(signature)} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(value)))
		body.Write(size[:])
		body.Write(value)
	}
	body.WriteByte(1)
	wire := make([]byte, body.Len()+4)
	binary.BigEndian.PutUint32(wire, uint32(body.Len()))
	copy(wire[4:], body.Bytes())
	return wire, agentNativePeer{version: "10.2", keyType: key.Type(), fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), certificate: certificate}
}

func TestNativeBindingRequiresExactKeyClassAndSignature(t *testing.T) {
	for _, certificate := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "certificate"}[certificate], func(t *testing.T) {
			wire, peer := nativeBindingFixture(t, certificate)
			if err := verifyNativeAgentRecord(wire, peer); err != nil {
				t.Fatal(err)
			}
			for _, change := range []string{"fingerprint", "key class", "log class", "signature", "forwarding flag", "trailing bytes", "length", "opcode", "extension"} {
				t.Run(change, func(t *testing.T) {
					input, expected := bytes.Clone(wire), peer
					switch change {
					case "fingerprint":
						expected.fingerprint = "SHA256:" + strings.Repeat("A", 43)
					case "key class":
						if certificate {
							expected.keyType = ssh.KeyAlgoED25519
						} else {
							expected.keyType = ssh.CertAlgoED25519v01
						}
					case "log class":
						expected.certificate = !expected.certificate
					case "signature":
						input[len(input)-2] ^= 1
					case "forwarding flag":
						input[len(input)-1] = 0
					case "trailing bytes":
						input = append(input, 0)
						binary.BigEndian.PutUint32(input[:4], uint32(len(input)-4))
					case "length":
						input[3] ^= 1
					case "opcode":
						input[4] = agentFWSign
					case "extension":
						input[9] ^= 1
					}
					if err := verifyNativeAgentRecord(input, expected); err == nil {
						t.Fatal("accepted a binding without the exact native key class and signed forwarding record")
					}
				})
			}
			for size := range len(wire) {
				if err := verifyNativeAgentRecord(wire[:size], peer); err == nil {
					t.Fatalf("accepted truncated binding at %d", size)
				}
			}
		})
	}
}

func TestNativePeerReadsCompletePrefixAndRejectsAmbiguity(t *testing.T) {
	_, peer := nativeBindingFixture(t, false)
	key := "debug1: Server host key: " + peer.keyType + " " + peer.fingerprint + "\r\n"
	base := "debug1: OpenSSH_10.2p1, test native contract\r\n" + key
	if got, err := parseAgentNativePeer(strings.NewReader(base)); err != nil || got != peer {
		t.Fatalf("native peer = %+v, %v", got, err)
	}
	for name, text := range map[string]string{
		"8.0":                  strings.Replace(base, "10.2p1", "8.0p1", 1),
		"8.8":                  strings.Replace(base, "10.2p1", "8.8p1", 1),
		"missing hook version": key,
		"missing key":          "debug1: OpenSSH_10.2p1, test\n",
		"malformed version":    base + "debug1: OpenSSH_bad\n",
		"malformed key":        base + "debug1: Server host key: malformed\n",
		"different version":    base + "debug1: OpenSSH_9.9p1, other\n",
		"different key":        base + strings.Replace(key, peer.fingerprint, "SHA256:"+strings.Repeat("B", 43), 1),
		"different class":      base + strings.Replace(key, peer.keyType, ssh.CertAlgoED25519v01, 1),
		"failure before key":   "debug2: get_agent_identities: ssh_agent_bind_hostkey: agent refused operation\n" + base,
		"failure after key":    base + "debug2: client_request_agent: ssh_agent_bind_hostkey: invalid format\n",
		"long later line":      base + strings.Repeat("x", 64<<10) + "\n",
		"host text alone":      "debug1: Remote: OpenSSH_10.2p1\n" + "debug1: Remote: " + strings.TrimSpace(key) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAgentNativePeer(strings.NewReader(text)); err == nil {
				t.Fatal("accepted an incomplete or ambiguous native prefix")
			}
		})
	}
	if _, err := parseAgentNativePeer(strings.NewReader(strings.Replace(base, "10.2p1", "8.9p1", 1))); err != nil {
		t.Fatalf("documented first native-binding contract refused: %v", err)
	}
}
