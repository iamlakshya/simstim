package slotlaunch

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

func TestReleaseKeyMatchesExistingTrustRoot(t *testing.T) {
	data, err := os.ReadFile("../../.github/release-signers")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ReleasePublicKey) {
		t.Fatal("launcher release key differs from decision 0017 trust root")
	}
}

func TestSignedEnvelopeRequiresExactRoleArchitectureVersionKeyAndNamespace(t *testing.T) {
	// This ephemeral in-memory key is a verifier fixture, never a release key.
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	public := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	meta := Envelope{Role: "launch", Arch: "amd64", Version: "1.2.3", SHA256: strings.Repeat("a", 64)}
	sign := func(data []byte, namespace string) []byte {
		t.Helper()
		signature, err := sshsig.Sign(bytes.NewReader(data), signer, sshsig.HashSHA512, namespace)
		if err != nil {
			t.Fatal(err)
		}
		return sshsig.Armor(signature)
	}
	raw, _ := json.Marshal(meta)
	signature := sign(raw, SignatureNamespace())
	if got, err := verifyEnvelope(raw, signature, public); err != nil || got != meta {
		t.Fatalf("valid signature: %+v %v", got, err)
	}
	if _, err := VerifyEnvelope(raw, signature); err == nil {
		t.Fatal("ephemeral key accepted as real release key")
	}
	if _, err := verifyEnvelope(append(raw, ' '), signature, public); err == nil {
		t.Fatal("changed signed bytes accepted")
	}
	if _, err := verifyEnvelope(raw, sign(raw, "file"), public); err == nil {
		t.Fatal("checksum namespace accepted for launcher")
	}
	for _, bad := range []string{`{"role":"launch","role":"launch","arch":"amd64","version":"1.2.3","sha256":"` + meta.SHA256 + `"}`, `{"Role":"launch","arch":"amd64","version":"1.2.3","sha256":"` + meta.SHA256 + `"}`, `{"role":"helper","arch":"amd64","version":"1.2.3","sha256":"` + meta.SHA256 + `"}`} {
		data := []byte(bad)
		if _, err := verifyEnvelope(data, sign(data, SignatureNamespace()), public); err == nil {
			t.Fatalf("accepted invalid signed envelope: %s", bad)
		}
	}
	if _, err := verifyEnvelope(raw, append(signature, signature...), public); err == nil {
		t.Fatal("multiple SSHSIG blocks accepted")
	}
}

func TestStrictSemanticVersionOrdering(t *testing.T) {
	for _, invalid := range []string{"v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3-", "1.2.3+"} {
		if releaseSemver(invalid) {
			t.Errorf("accepted %q", invalid)
		}
	}
	ordered := []string{"1.2.3-alpha", "1.2.3-alpha.1", "1.2.3-alpha.beta", "1.2.3-beta", "1.2.3-beta.2", "1.2.3-beta.11", "1.2.3-rc.1", "1.2.3", "1.2.4", "1.10.0", "2.0.0"}
	for i := 1; i < len(ordered); i++ {
		if compareVersion(ordered[i-1], ordered[i]) >= 0 {
			t.Errorf("%s did not precede %s", ordered[i-1], ordered[i])
		}
	}
	if compareVersion("1.2.3+build.2", "1.2.3+build.1") != 0 {
		t.Fatal("build metadata changed version precedence")
	}
}
