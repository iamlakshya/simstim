package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/brand"
)

const editorSSHHostKeyNameV1 = "ssh-host-key"

// SSHHostKey owns one parsed local host key until all of its server's work
// joins. Its key bytes are never an identity or agent forwarded to a host.
type SSHHostKey struct {
	mu     sync.Mutex
	key    ed25519.PrivateKey
	signer ssh.Signer
	public ssh.PublicKey
	closed bool
}

func editorSSHKeyErrorV1() error {
	return &UserError{Msg: "the local SSH host key is absent, changed or invalid", Hint: "Check its ownership and mode 0600, then run " + brand.Name + " ssh-config.", Code: "ssh_host_key", Kind: KindRefused}
}

// LoadSSHHostKey opens the existing native-owned state file. Starting a proxy
// never silently creates or replaces its pin.
func LoadSSHHostKey(ctx context.Context) (*SSHHostKey, error) {
	return editorSSHHostKeyV1(ctx, nil)
}

func editorSSHHostKeyV1(ctx context.Context, probe *editorSSHProbeV1) (_ *SSHHostKey, returned error) {
	if probe != nil {
		if err := probe.check(ctx); err != nil {
			return nil, err
		}
	}
	var result *SSHHostKey
	var encoded []byte
	defer func() { clear(encoded) }()
	defer func() {
		if returned != nil && result != nil {
			_ = result.Close()
		}
	}()
	err := withStateFileFD(ctx, nil, editorSSHHostKeyNameV1, probe != nil, func(view stateFileView) (stateFileReplace, error) {
		if view.Exists {
			if view.Size <= 0 || view.Size > 16<<10 {
				return nil, editorSSHKeyErrorV1()
			}
			data := make([]byte, int(view.Size))
			defer clear(data)
			if _, err := io.ReadFull(io.NewSectionReader(view.ReaderAt, 0, view.Size), data); err != nil {
				return nil, err
			}
			key, err := ssh.ParseRawPrivateKey(data)
			if err != nil {
				return nil, editorSSHKeyErrorV1()
			}
			private, ok := key.(*ed25519.PrivateKey)
			if !ok || private == nil || len(*private) != ed25519.PrivateKeySize {
				return nil, editorSSHKeyErrorV1()
			}
			result, err = newEditorSSHHostKeyV1(*private)
			clear(*private)
			return nil, err
		}
		if probe == nil {
			return nil, editorSSHKeyErrorV1()
		}
		if err := probe.check(ctx); err != nil {
			return nil, err
		}
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		defer clear(private)
		result, err = newEditorSSHHostKeyV1(private)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, "")
		if err != nil {
			return nil, err
		}
		encoded = pem.EncodeToMemory(block)
		clear(block.Bytes)
		return func(owned context.Context, out io.Writer) error {
			defer clear(encoded)
			if err := probe.check(owned); err != nil {
				return err
			}
			_, err := out.Write(encoded)
			return err
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, editorSSHKeyErrorV1()
	}
	return result, nil
}

func newEditorSSHHostKeyV1(private ed25519.PrivateKey) (*SSHHostKey, error) {
	owned := append(ed25519.PrivateKey(nil), private...)
	signer, err := ssh.NewSignerFromKey(owned)
	if err != nil {
		clear(owned)
		return nil, err
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		clear(owned)
		return nil, editorSSHKeyErrorV1()
	}
	public, err := ssh.ParsePublicKey(signer.PublicKey().Marshal())
	if err != nil {
		clear(owned)
		return nil, err
	}
	return &SSHHostKey{key: owned, signer: signer, public: public}, nil
}

func (k *SSHHostKey) PublicKey() ssh.PublicKey {
	if k == nil {
		return nil
	}
	return k.public
}

func (k *SSHHostKey) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	if k == nil {
		return nil, editorSSHKeyErrorV1()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed || k.signer == nil {
		return nil, editorSSHKeyErrorV1()
	}
	return k.signer.Sign(random, data)
}

func (k *SSHHostKey) Close() error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	clear(k.key)
	k.key, k.signer = nil, nil
	k.closed = true
	return nil
}

var _ ssh.Signer = (*SSHHostKey)(nil)
