package guest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func publicTrustFixture(t *testing.T, name string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func trustGuestFixture(t *testing.T) (*Init, []byte) {
	t.Helper()
	g := &Init{root: t.TempDir()}
	roots := publicTrustFixture(t, "Existing image authority")
	for _, dir := range []string{g.path("/etc/ssl/certs"), g.path(host.GuestRunDir())} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(g.path("/etc/ssl/certs/ca-certificates.crt"), roots, 0o600); err != nil {
		t.Fatal(err)
	}
	return g, roots
}

func TestGuestSessionTrustKeepsSystemRootsAndAgentCAEnvironment(t *testing.T) {
	g, roots := trustGuestFixture(t)
	public := publicTrustFixture(t, "Session authority")
	args := proto.CtlStartArgs{Agent: "codex", Proxy: "http://127.0.0.1:3128", CACert: string(public),
		Env: []string{"SSL_CERT_FILE=/untrusted", "CODEX_CA_CERTIFICATE=/untrusted", "KEEP=value"}}
	if err := g.installSessionTrust(&args); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(host.GuestRunDir(), "ca.pem")
	bundlePath := filepath.Join(host.GuestRunDir(), "ca-bundle.pem")
	ca, err := os.ReadFile(g.path(caPath))
	if err != nil || !bytes.Equal(ca, public) {
		t.Fatalf("public CA = %q, %v", ca, err)
	}
	bundle, err := os.ReadFile(g.path(bundlePath))
	if err != nil || !bytes.Contains(bundle, roots) || !bytes.Contains(bundle, public) || bytes.Contains(bundle, []byte("PRIVATE KEY")) {
		t.Fatalf("bundle lost existing roots or included signing material: %v", err)
	}
	for key, want := range map[string]string{"SSL_CERT_FILE": bundlePath, "REQUESTS_CA_BUNDLE": bundlePath,
		"GIT_SSL_CAINFO": bundlePath, "CODEX_CA_CERTIFICATE": bundlePath, "NODE_EXTRA_CA_CERTS": caPath, "KEEP": "value"} {
		count := 0
		for _, entry := range args.Env {
			if strings.HasPrefix(entry, key+"=") {
				count++
				if entry != key+"="+want {
					t.Fatalf("wrong trust variable: %s", entry)
				}
			}
		}
		if count != 1 {
			t.Fatalf("%s appears %d times", key, count)
		}
	}
	for _, path := range []string{caPath, bundlePath} {
		info, err := os.Stat(g.path(path))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("trust file permissions: %v, %v", info, err)
		}
	}
}

func TestGuestSessionTrustRejectsPrivateOrMalformedMaterial(t *testing.T) {
	for _, cert := range []string{"", "not a certificate", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("invalid")})), strings.Repeat("x", (8<<10)+1)} {
		g, _ := trustGuestFixture(t)
		args := proto.CtlStartArgs{Proxy: "http://127.0.0.1:3128", CACert: cert}
		if err := g.installSessionTrust(&args); err == nil {
			t.Fatal("invalid trust material accepted")
		}
		if _, err := os.Stat(g.path(filepath.Join(host.GuestRunDir(), "ca.pem"))); !os.IsNotExist(err) {
			t.Fatalf("failed trust left a certificate: %v", err)
		}
	}
	g, _ := trustGuestFixture(t)
	if err := os.Remove(g.path("/etc/ssl/certs/ca-certificates.crt")); err != nil {
		t.Fatal(err)
	}
	args := proto.CtlStartArgs{CACert: string(publicTrustFixture(t, "Session authority"))}
	if err := g.installSessionTrust(&args); err == nil {
		t.Fatal("session CA replaced missing system roots")
	}
}

func TestGuestTrustWriteReplacesFinalSymlinkWithoutTouchingItsTarget(t *testing.T) {
	g, _ := trustGuestFixture(t)
	target := filepath.Join(g.root, "untouched")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := g.path(filepath.Join(host.GuestRunDir(), "ca.pem"))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	args := proto.CtlStartArgs{CACert: string(publicTrustFixture(t, "Session authority"))}
	if err := g.installSessionTrust(&args); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("trust installation followed a final symlink: %q, %v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("trust certificate stayed a symlink: %v", err)
	}
}

func TestGuestTrustNeverReadsASystemBundleOutsideItsAssignedRoot(t *testing.T) {
	g, _ := trustGuestFixture(t)
	path := g.path("/etc/ssl/certs/ca-certificates.crt")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-roots.pem")
	if err := os.WriteFile(outside, publicTrustFixture(t, "Outside authority"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	args := proto.CtlStartArgs{CACert: string(publicTrustFixture(t, "Session authority"))}
	if err := g.installSessionTrust(&args); err == nil {
		t.Fatal("guest trust read outside its assigned filesystem root")
	}
	if _, err := os.Stat(g.path(filepath.Join(host.GuestRunDir(), "ca.pem"))); !os.IsNotExist(err) {
		t.Fatalf("rejected trust wrote a CA: %v", err)
	}
}

func TestGuestTrustEnvForJoinedAgentKeepsOneAuthority(t *testing.T) {
	g, _ := trustGuestFixture(t)
	args := proto.CtlStartArgs{Agent: "shell", CACert: string(publicTrustFixture(t, "Session authority"))}
	if err := g.installSessionTrust(&args); err != nil {
		t.Fatal(err)
	}
	caPath := g.path(filepath.Join(host.GuestRunDir(), "ca.pem"))
	before, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	env, err := g.childTrustEnv(append(args.Env, "CODEX_CA_CERTIFICATE=/untrusted"), "codex")
	if err != nil {
		t.Fatal(err)
	}
	want := "CODEX_CA_CERTIFICATE=" + filepath.Join(host.GuestRunDir(), "ca-bundle.pem")
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "CODEX_CA_CERTIFICATE=") {
			count++
			if entry != want {
				t.Fatalf("joined agent trust variable = %q", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("joined agent has %d CA variables", count)
	}
	after, err := os.ReadFile(caPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("joining another agent changed the session public authority")
	}
	if got, err := (&Init{}).childTrustEnv([]string{"KEEP=value"}, "codex"); err != nil || len(got) != 1 || got[0] != "KEEP=value" {
		t.Fatalf("uninitialised test guest invented trust files: %v, %v", got, err)
	}
}
