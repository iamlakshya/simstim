// Package sshserve is the client-side SSH server for `ssh-proxy` and the
// `ssh-config` generator (spec 17.7). SSH ends in the client on stdio using
// golang.org/x/crypto/ssh, so editors (VS Code, Cursor, Zed), scp, sftp, rsync
// and plain ssh reach a session without any SSH daemon listening in the
// session or on the host.
package sshserve

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/brand"
)

// HostKeyPath is the path to the laptop's Ed25519 host key for ssh-proxy.
func HostKeyPath(stateDir string) string {
	return filepath.Join(stateDir, "ssh-host-key")
}

// EnsureHostKey loads the laptop's Ed25519 host key from stateDir, generating
// and saving it (mode 0600) when missing.
func EnsureHostKey(stateDir string) (ssh.Signer, error) {
	p := HostKeyPath(stateDir)
	if b, err := os.ReadFile(p); err == nil {
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("parse SSH host key %s: %w", p, err)
		}
		return signer, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read SSH host key %s: %w", p, err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state folder %s: %w", stateDir, err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Ed25519 host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, brand.Name+" ssh-proxy host key")
	if err != nil {
		return nil, fmt.Errorf("encode SSH host key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(block)
	tmp, err := os.CreateTemp(stateDir, ".ssh-host-key-*")
	if err != nil {
		return nil, fmt.Errorf("create temp host key: %w", err)
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("chmod host key: %w", err)
	}
	if _, err := tmp.Write(pemBytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("write host key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("close host key: %w", err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("save host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("build SSH host signer: %w", err)
	}
	return signer, nil
}

// KnownHostsLine formats the one known_hosts entry that matches every session
// name (*.<brand.Name>) for signer's public key.
func KnownHostsLine(signer ssh.Signer) string {
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return "*." + brand.Name + " " + pub + "\n"
}
