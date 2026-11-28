package guest

import (
	"bytes"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestWriteSessionCAAndEnvVars(t *testing.T) {
	ca, err := egress.NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA: %v", err)
	}
	defer ca.Close()

	runDir := t.TempDir()
	caPath, bundlePath, err := WriteSessionCA(runDir, ca.CertPEM())
	if err != nil {
		t.Fatalf("WriteSessionCA: %v", err)
	}

	caBytes, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read ca.pem: %v", err)
	}
	if !bytes.Contains(caBytes, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("ca.pem missing BEGIN CERTIFICATE")
	}
	if bytes.Contains(bytes.ToUpper(caBytes), []byte("PRIVATE KEY")) {
		t.Fatal("ca.pem must never contain PRIVATE KEY")
	}

	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read ca-bundle.pem: %v", err)
	}
	if !bytes.Contains(bundleBytes, bytes.TrimSpace(caBytes)) {
		t.Fatal("ca-bundle.pem does not contain session CA certificate")
	}
	// Only CERTIFICATE blocks: the system bundle may hold text, such as the
	// "Private Key Usage Period" extension name on macOS, but no key block.
	for rest := bundleBytes; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			t.Fatalf("ca-bundle.pem holds a %s block", block.Type)
		}
	}
	if bytes.Contains(bundleBytes, []byte("Private Key Usage Period")) {
		t.Fatal("ca-bundle.pem kept the system bundle's text; want PEM blocks only")
	}

	// Verify agent CAEnv wiring for Claude Code and Codex.
	claude, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude agent missing")
	}
	claudeEnv := CAEnvVars(caPath, bundlePath, claude)
	if claudeEnv["NODE_EXTRA_CA_CERTS"] != caPath {
		t.Fatalf("claude NODE_EXTRA_CA_CERTS = %q, want %q", claudeEnv["NODE_EXTRA_CA_CERTS"], caPath)
	}
	for _, k := range []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO", "CURL_CA_BUNDLE"} {
		if claudeEnv[k] != bundlePath {
			t.Fatalf("claude %s = %q, want %q", k, claudeEnv[k], bundlePath)
		}
	}

	codex, ok := agents.Find("codex")
	if !ok {
		t.Fatal("codex agent missing")
	}
	codexEnv := CAEnvVars(caPath, bundlePath, codex)
	if codexEnv["CODEX_CA_CERTIFICATE"] != bundlePath {
		t.Fatalf("codex CODEX_CA_CERTIFICATE = %q, want %q", codexEnv["CODEX_CA_CERTIFICATE"], bundlePath)
	}
	if codexEnv["SSL_CERT_FILE"] != bundlePath {
		t.Fatalf("codex SSL_CERT_FILE = %q, want %q", codexEnv["SSL_CERT_FILE"], bundlePath)
	}
}

func TestWriteSessionCARejectsPrivateKeyAndBadPEM(t *testing.T) {
	runDir := t.TempDir()
	cases := []struct {
		name string
		pem  string
	}{
		{"private key", "-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n"},
		{"empty", ""},
		{"garbage", "not a pem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := WriteSessionCA(runDir, tc.pem); err == nil {
				t.Fatalf("WriteSessionCA(%s) succeeded, want error", tc.name)
			} else if !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error missing Try hint: %v", err)
			}
		})
	}
}

func TestApplyStartCAMicroVMPreservesDirMode(t *testing.T) {
	ca, err := egress.NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA: %v", err)
	}
	defer ca.Close()

	runDir := t.TempDir() + "/run"
	if err := chownMicroVMDir(runDir, 0, 0, 0o755); err != nil {
		t.Fatalf("chownMicroVMDir: %v", err)
	}
	if err := os.Chmod(runDir, 0o755); err != nil {
		t.Fatalf("chmod runDir: %v", err)
	}
	env, err := applyStartCA(runDir, true, StartReq{Agent: "codex", CACertPEM: ca.CertPEM()}, nil)
	if err != nil {
		t.Fatalf("applyStartCA microvm: %v", err)
	}
	if len(env) == 0 {
		t.Fatal("expected CA env vars to be populated")
	}
	di, err := os.Stat(runDir)
	if err != nil {
		t.Fatalf("stat runDir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o755 {
		t.Fatalf("microvm runDir perm = %04o, want 0755", got)
	}
	for _, name := range []string{"ca.pem", "ca-bundle.pem"} {
		fi, err := os.Stat(runDir + "/" + name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := fi.Mode().Perm(); got != 0o644 {
			t.Fatalf("%s perm = %04o, want 0644", name, got)
		}
	}
}
