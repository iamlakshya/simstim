//go:build linux

package host

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

type ordinaryEndpointV1 struct {
	root     *os.Root
	rootRef  cachefs.Reference
	name     string
	original os.FileInfo
	removed  bool
}

func bindOrdinaryHolderSocketsV1(owner *ownedOrdinaryHolderV1, h *Holder) error {
	if owner == nil || h == nil {
		return sessionLeaseError("the actual ordinary socket creator is absent")
	}
	h.ordinary = owner
	socket, hook := h.sockets()
	for i, name := range []string{socket, hook} {
		listener := h.ln
		if i == 1 {
			listener = h.hookLn
		}
		native, ok := listener.(*net.UnixListener)
		if !ok {
			return sessionLeaseError("the actual ordinary holder has no native Unix listener")
		}
		// net.UnixListener otherwise unlinks a name on Close without checking
		// whether it still names this creator's original socket.
		native.SetUnlinkOnClose(false)
		root, err := cachefs.OpenPrivateRoot(filepath.Dir(name), false)
		if err != nil {
			return err
		}
		ref, err := cachefs.CaptureRoot(root)
		if err != nil {
			return errors.Join(err, root.Close())
		}
		actual, err := root.Lstat(filepath.Base(name))
		if err != nil || actual.Mode()&os.ModeSocket == 0 || actual.Mode().Perm() != 0o600 {
			return errors.Join(sessionLeaseError("the actual ordinary socket lost its original private binding"), err, root.Close())
		}
		owner.endpoints = append(owner.endpoints, &ordinaryEndpointV1{root: root, rootRef: ref, name: filepath.Base(name), original: actual})
	}
	return nil
}

func (h *Holder) closeOriginalOrdinarySocketsV1() error {
	owner := h.ordinary
	if owner == nil {
		return sessionLeaseError("the ordinary socket closure has no original creator")
	}
	owner.socketMu.Lock()
	defer owner.socketMu.Unlock()
	if owner.socketsClosed {
		return nil
	}
	for _, listener := range []net.Listener{h.ln, h.hookLn} {
		if listener != nil {
			_ = listener.Close()
		}
	}
	if len(owner.endpoints) != 2 {
		return sessionLeaseError("the ordinary holder has no exact original endpoint pair")
	}
	for _, endpoint := range owner.endpoints {
		if endpoint.removed {
			continue
		}
		if err := endpoint.rootRef.CheckRoot(endpoint.root); err != nil {
			return err
		}
		actual, err := endpoint.root.Lstat(endpoint.name)
		if err != nil || !os.SameFile(actual, endpoint.original) {
			return errors.Join(sessionLeaseError("the original ordinary endpoint was replaced; preserve its current name"), err)
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		quarantine := ".ordinary-stop-" + hex.EncodeToString(nonce[:])
		directory, err := endpoint.root.Open(".")
		if err != nil {
			return err
		}
		err = unix.Renameat2(int(directory.Fd()), endpoint.name, int(directory.Fd()), quarantine, unix.RENAME_NOREPLACE)
		err = errors.Join(err, directory.Close())
		if err != nil {
			return err
		}
		moved, err := endpoint.root.Lstat(quarantine)
		if err != nil || !os.SameFile(moved, endpoint.original) {
			// A concurrent replacement is retained in this checked parent,
			// never unlinked as if it belonged to the original creator.
			return errors.Join(sessionLeaseError("the socket changed during closure; preserve its quarantined object"), err)
		}
		if err := endpoint.root.Remove(quarantine); err != nil {
			return err
		}
		endpoint.removed = true
	}
	owner.socketsClosed = true
	return nil
}
