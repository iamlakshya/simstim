package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

const hostTerminalPrefix = "host-state-retirement/1\n"

// hostOperationFile is one pinned existing or exclusively created OP inode.
// It owns no state folder. Terminal retry will use a separate existing-only
// entry and must never call this normal acquisition to create a replacement.
type hostOperationFile struct {
	root        *os.Root
	rootPath    string
	stateDir    string
	name        string
	source      hostStateSource
	observation *HostStateObservation
	parent      cachefs.Reference
	file        *os.File
	reference   cachefs.Reference
	who         *cachefs.Reference
}

// openHostOperationFile holds membership while deciding whether creation is
// allowed. Its caller releases membership before the nonblocking OS lock,
// then repeats checkAfterTake under membership before exposing the owner.
func openHostOperationFile(ctx context.Context, name string) (*hostOperationFile, error) {
	var owned *hostOperationFile
	err := withHostMembershipContext(ctx, func(locked context.Context) error {
		var source hostStateSource
		if o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); o != nil {
			if o.name != name {
				return hostStateRemovalError("the OP admission belongs to another host")
			}
			if err := o.checkMembership(locked); err != nil {
				return err
			}
			source = o.source
		} else {
			// Normal admission is for an actually saved host. An absent name
			// requires a prior explicit registration or publication intent.
			hosts, err := LoadHosts()
			if err != nil {
				return err
			}
			i := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == name })
			if i < 0 {
				_, err := FindHost(name)
				return err
			}
			source, err = captureHostStateSource(locked, hosts, true)
			if err != nil {
				return err
			}
		}
		parent := filepath.Dir(hostLockPath(name))
		root, err := cachefs.OpenPrivateRoot(parent, true)
		if err != nil {
			return err
		}
		keep := false
		defer func() {
			if !keep {
				_ = root.Close()
			}
		}()
		parentRef, err := cachefs.CaptureRoot(root)
		if err != nil {
			return err
		}
		leaf := filepath.Base(hostLockPath(name))
		file, err := cachefs.OpenFile(root, leaf, os.O_RDWR, 0600)
		if errors.Is(err, os.ErrNotExist) {
			if err := locked.Err(); err != nil {
				return err
			}
			file, err = cachefs.OpenFile(root, leaf, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		}
		if err != nil {
			return err
		}
		reference, err := cachefs.CaptureFile(root, leaf, file, 0600)
		if err != nil {
			_ = file.Close()
			return err
		}
		observation, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
		owned = &hostOperationFile{root: root, rootPath: parent, stateDir: StateDir(), name: leaf,
			source: source, parent: parentRef, file: file, reference: reference}
		owned.observation = observation
		keep = true
		return owned.normalHeader(locked)
	})
	if err != nil && owned != nil {
		owned.close()
	}
	return owned, err
}

func (f *hostOperationFile) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.stateDir != StateDir() || f.rootPath != filepath.Join(StateDir(), "hosts") {
		return hostStateRemovalError("the host operation root changed")
	}
	ambient, err := cachefs.OpenPrivateRoot(f.rootPath, false)
	if err != nil {
		return err
	}
	defer ambient.Close()
	if err := f.parent.CheckRoot(ambient); err != nil {
		return err
	}
	if err := f.parent.CheckRoot(f.root); err != nil {
		return err
	}
	return f.reference.CheckCreatedFile(f.root, f.name, f.file, 0600)
}

func (f *hostOperationFile) normalHeader(ctx context.Context) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	info, err := f.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	// This is only bounded refusal classification. It does not decode a
	// receipt or turn a partial prefix into terminal cleanup authority.
	var prefix [len(hostTerminalPrefix)]byte
	n, err := f.file.ReadAt(prefix[:], 0)
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n == len(prefix) && string(prefix[:]) == hostTerminalPrefix {
		return hostStateRemovalError("this host has an unfinished local state retirement")
	}
	return hostStateRemovalError("the host operation file has unknown nonempty contents")
}

func (f *hostOperationFile) checkAfterTake(ctx context.Context, hostName, kind string, held *heldLock) error {
	return withHostMembershipContext(ctx, func(locked context.Context) error {
		if f.observation != nil {
			if err := f.observation.checkOwner(locked); err != nil {
				return err
			}
		}
		if err := f.source.check(locked); err != nil {
			return err
		}
		if err := f.normalHeader(locked); err != nil {
			return err
		}
		if observation := f.observation; observation != nil && observation.role == hostStatePublication {
			if !observation.publicationOP.CompareAndSwap(nil, held) {
				return hostStateRemovalError("the metadata publication already used another operation")
			}
		}
		if observation := f.observation; observation != nil && observation.role == hostStateCreation {
			if observation.creation == nil || !observation.creation.op.CompareAndSwap(nil, held) {
				return hostStateRemovalError("the creation reservation already used another operation")
			}
		}
		// Diagnostic creation is after complete Phase C revalidation and
		// while membership is still held. Failure grants no diagnostic
		// ownership, but does not invalidate the real OP lock.
		_ = f.writeDiagnostic(locked, kind)
		return locked.Err()
	})
}

func (f *hostOperationFile) writeDiagnostic(ctx context.Context, kind string) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	name := f.name + ".who"
	file, err := cachefs.OpenFile(f.root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		// A diagnostic left by a different owner is not ours to replace or
		// delete. It grants no lock authority and is never trusted as one.
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	created, err := cachefs.CaptureFile(f.root, name, file, 0600)
	if err != nil {
		return err
	}
	var success bool
	defer func() {
		if !success && f.check(context.WithoutCancel(ctx)) == nil && created.CheckCreatedFile(f.root, name, file, 0600) == nil {
			_ = f.root.Remove(name)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if n, err := io.WriteString(file, kind); err != nil {
		return err
	} else if n != len(kind) {
		return io.ErrShortWrite
	}
	reference, err := cachefs.CaptureFile(f.root, name, file, 0600)
	if err != nil {
		return err
	}
	f.who = &reference
	success = true
	return nil
}

func (f *hostOperationFile) removeOwnedDiagnostic() {
	if f.who == nil || f.check(context.Background()) != nil {
		return
	}
	name := f.name + ".who"
	file, err := cachefs.OpenFile(f.root, name, os.O_RDONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	if f.who.CheckFile(f.root, name, file, 0600) == nil {
		_ = f.root.Remove(name)
	}
}

func (f *hostOperationFile) close() {
	_ = f.file.Close()
	_ = f.root.Close()
}
