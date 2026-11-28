package guest

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/paths"
)

// systemCABundlePaths lists common system root CA bundle locations searched
// inside the sandbox when building ca-bundle.pem (spec 7.2).
var systemCABundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// WriteSessionCA writes the session's public CA certificate to <runDir>/ca.pem
// and a combined bundle of the image's system CA roots plus the session CA to
// <runDir>/ca-bundle.pem (spec 7.2). It refuses any payload containing private
// key material or anything other than a single valid X.509 certificate.
func WriteSessionCA(runDir, certPEM string) (caPath, bundlePath string, err error) {
	return writeSessionCA(runDir, certPEM, false)
}

func writeSessionCA(runDir, certPEM string, microVM bool) (caPath, bundlePath string, err error) {
	cleaned, err := validatePublicCertPEM(certPEM)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Clean(runDir)
	if microVM {
		if err := chownMicroVMDir(dir, 0, 0, 0o755); err != nil {
			return "", "", fmt.Errorf("guest ca: prepare microvm runtime dir %s: %w. Try: check sandbox runtime permissions", dir, err)
		}
	} else if err := paths.EnsurePrivateDir(dir); err != nil {
		return "", "", fmt.Errorf("guest ca: create runtime dir %s: %w. Try: check sandbox runtime permissions", dir, err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", "", fmt.Errorf("guest ca: open runtime dir %s: %w. Try: check sandbox runtime permissions", dir, err)
	}
	defer func() { _ = root.Close() }()

	if err := writeAtomicRootFile(root, "ca.pem", cleaned, 0o644); err != nil {
		return "", "", fmt.Errorf("guest ca: write ca.pem: %w. Try: check sandbox runtime permissions", err)
	}

	var bundle bytes.Buffer
	if sysBundle := readSystemCABundle(systemCABundlePaths); len(sysBundle) > 0 {
		bundle.Write(sysBundle)
		if sysBundle[len(sysBundle)-1] != '\n' {
			bundle.WriteByte('\n')
		}
	}
	bundle.Write(cleaned)

	if err := writeAtomicRootFile(root, "ca-bundle.pem", bundle.Bytes(), 0o644); err != nil {
		return "", "", fmt.Errorf("guest ca: write ca-bundle.pem: %w. Try: check sandbox runtime permissions", err)
	}
	return filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-bundle.pem"), nil
}

// CAEnvVars returns the environment variables that point common runtimes,
// package managers and agent CLIs at the session's public CA certificate and
// combined system CA bundle (spec 7.2). NODE_EXTRA_CA_CERTS receives ca.pem
// alone so Node keeps its built-in roots; SSL_CERT_FILE, REQUESTS_CA_BUNDLE,
// GIT_SSL_CAINFO, CURL_CA_BUNDLE and agent.CAEnv entries receive ca-bundle.pem
// (or ca.pem for NODE_EXTRA_CA_CERTS).
func CAEnvVars(caPath, bundlePath string, agent agents.Agent) map[string]string {
	if caPath == "" || bundlePath == "" {
		return nil
	}
	env := map[string]string{
		"SSL_CERT_FILE":       bundlePath,
		"REQUESTS_CA_BUNDLE":  bundlePath,
		"GIT_SSL_CAINFO":      bundlePath,
		"CURL_CA_BUNDLE":      bundlePath,
		"NODE_EXTRA_CA_CERTS": caPath,
	}
	for _, key := range agent.CAEnv {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if k == "NODE_EXTRA_CA_CERTS" {
			env[k] = caPath
		} else {
			env[k] = bundlePath
		}
	}
	return env
}

func applyStartCA(runDir string, microVM bool, a StartReq, env []string) ([]string, error) {
	if strings.TrimSpace(a.CACertPEM) == "" {
		return env, nil
	}
	caPath, bundlePath, err := writeSessionCA(runDir, a.CACertPEM, microVM)
	if err != nil {
		return nil, err
	}
	agent, _ := agents.Find(a.Agent)
	caEnv := CAEnvVars(caPath, bundlePath, agent)
	keys := make([]string, 0, len(caEnv))
	for k := range caEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = withEnvVar(env, k, caEnv[k])
	}
	return env, nil
}

func validatePublicCertPEM(raw string) ([]byte, error) {
	if strings.Contains(strings.ToUpper(raw), "PRIVATE KEY") {
		return nil, errors.New("guest ca: refused CA payload containing private key material. Try: pass only the public session CA certificate")
	}
	block, rest := pem.Decode([]byte(raw))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("guest ca: invalid CA certificate PEM. Try: pass a single public X.509 CERTIFICATE block")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("guest ca: unexpected extra PEM data in session CA. Try: pass a single public X.509 CERTIFICATE block")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("guest ca: parse X.509 certificate: %w. Try: restart the session to generate a fresh CA", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}), nil
}

// readSystemCABundle returns the CERTIFICATE blocks of the first system
// bundle that has any, re-encoded as PEM. Anything else in the file, such as
// macOS's text descriptions or a stray key, never reaches the sandbox.
func readSystemCABundle(candidates []string) []byte {
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if certs := certificateBlocks(data); len(certs) > 0 {
			return certs
		}
	}
	return nil
}

// certificateBlocks keeps only the PEM blocks of type CERTIFICATE in data.
func certificateBlocks(data []byte) []byte {
	var out bytes.Buffer
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return out.Bytes()
		}
		if block.Type == "CERTIFICATE" && len(block.Headers) == 0 {
			_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})
		}
	}
}

func writeAtomicRootFile(root *os.Root, name string, data []byte, perm os.FileMode) error {
	tmp := name + ".tmp"
	if err := root.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := root.Chmod(tmp, perm); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("install %s: %w", name, err)
	}
	return nil
}
