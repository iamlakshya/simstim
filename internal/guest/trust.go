package guest

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/memberenv"
	"github.com/iamlakshya/simstim/internal/proto"
)

const maxSystemTrustBytes = 8 << 20

func (g *Init) installSessionTrust(a *proto.CtlStartArgs) error {
	if a.CACert == "" {
		if a.Proxy == "" {
			return nil
		}
		return trustError("the sandbox proxy has no session CA", nil)
	}
	if len(a.CACert) > 8<<10 {
		return trustError("the session CA certificate is too large", nil)
	}
	block, rest := pem.Decode([]byte(a.CACert))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return trustError("the session CA is not one public certificate", nil)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return trustError("the session CA cannot sign destination certificates", err)
	}
	roots, err := g.systemTrust()
	if err != nil {
		return err
	}
	caPath := filepath.Join(host.GuestRunDir(), "ca.pem")
	bundlePath := filepath.Join(host.GuestRunDir(), "ca-bundle.pem")
	if err := g.writeGuestTrust(caPath, []byte(a.CACert)); err != nil {
		return trustError("cannot install the public session CA", err)
	}
	bundle := append(roots, '\n')
	bundle = append(bundle, a.CACert...)
	if err := g.writeGuestTrust(bundlePath, bundle); err != nil {
		return trustError("cannot install the session trust bundle", err)
	}
	env, err := sessionTrustEnvironment(a.Env, a.Agent)
	if err != nil {
		return err
	}
	a.Env = env
	g.mu.Lock()
	g.caReady = true
	g.mu.Unlock()
	return nil
}

// childTrustEnv reuses the session public trust files for a joined agent.
// It never creates another authority or receives signing material.
func (g *Init) childTrustEnv(env []string, kind string) ([]string, error) {
	g.mu.Lock()
	ready := g.caReady
	g.mu.Unlock()
	if !ready {
		return append([]string(nil), env...), nil
	}
	return sessionTrustEnvironment(env, kind)
}

func sessionTrustEnvironment(input []string, kind string) ([]string, error) {
	return memberenv.Trust(input, kind, filepath.Join(host.GuestRunDir(), "ca.pem"), filepath.Join(host.GuestRunDir(), "ca-bundle.pem"))
}

func (g *Init) systemTrust() ([]byte, error) {
	root, err := g.trustRoot()
	if err != nil {
		return nil, trustError("cannot open the guest trust root", err)
	}
	defer root.Close()
	for _, name := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt"} {
		relative := strings.TrimPrefix(name, "/")
		info, err := root.Stat(relative)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSystemTrustBytes {
			return nil, trustError("cannot read the guest system roots", err)
		}
		f, err := root.Open(relative)
		if err != nil {
			return nil, trustError("cannot read the guest system roots", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxSystemTrustBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return nil, trustError("cannot read the guest system roots", errors.Join(readErr, closeErr))
		}
		if len(data) > maxSystemTrustBytes {
			return nil, trustError("the guest system roots exceed the trust bundle limit", nil)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, trustError("the guest system roots contain no certificates", nil)
		}
		return data, nil
	}
	return nil, trustError("the guest has no system root certificate bundle", nil)
}

func trustError(message string, _ error) error {
	return &proto.Error{Code: "wall_trust", Message: message,
		Hint: "Use a guest image with system CA certificates, then resume the session."}
}

func (g *Init) trustRoot() (*os.Root, error) {
	path := g.root
	if path == "" {
		path = "/"
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open guest trust root: %w", err)
	}
	return root, nil
}

func (g *Init) writeGuestTrust(path string, data []byte) error {
	root, err := g.trustRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	parent, err := root.OpenRoot(strings.TrimPrefix(filepath.Dir(path), "/"))
	if err != nil {
		return fmt.Errorf("open guest runtime folder: %w", err)
	}
	defer parent.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("name guest trust file: %w", err)
	}
	name := ".trust-" + hex.EncodeToString(random[:])
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create guest trust file: %w", err)
	}
	defer parent.Remove(name)
	if g.vm != nil {
		if err := file.Chmod(0o644); err != nil {
			_ = file.Close()
			return fmt.Errorf("make public guest trust readable: %w", err)
		}
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write guest trust file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close guest trust file: %w", err)
	}
	// Rename replaces a stale final symlink instead of following its target.
	// The private runtime folder is made before the guest agent starts.
	if err := parent.Rename(name, filepath.Base(path)); err != nil {
		return fmt.Errorf("install guest trust file: %w", err)
	}
	return nil
}
