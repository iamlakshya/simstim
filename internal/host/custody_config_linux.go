//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// custodyConfigV1 serialises the real user's manager reloads and retained-FD
// admissions. Its lock is independent of OP; supervisors never acquire OP
// while holding it. Every caller retains the same authenticated manager.
type custodyConfigV1 struct {
	manager *nativeUserManagerV1
	lock    *os.File
	lockRef cachefs.Reference
	root    *os.Root
	rootRef cachefs.Reference
}

func (manager *nativeUserManagerV1) lockCustodyConfigV1(ctx context.Context) (_ *custodyConfigV1, failure error) {
	if err := errors.Join(ctx.Err(), manager.checkNativeManager()); err != nil {
		return nil, err
	}
	name := brand.Name + "-custody-config.lock"
	file, err := cachefs.OpenFile(manager.root, name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	config := &custodyConfigV1{manager: manager, lock: file}
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, config.Close())
		}
	}()
	config.lockRef, err = cachefs.CaptureFile(manager.root, name, file, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		held, err := cachefs.TryLock(file, cachefs.Exclusive)
		if err != nil {
			return nil, err
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	config.root, err = cachefs.OpenPrivateRoot(filepath.Join(manager.rootPath, "systemd", "user"), true)
	if err != nil {
		return nil, err
	}
	config.rootRef, err = cachefs.CaptureRoot(config.root)
	if err != nil {
		return nil, err
	}
	if err := config.check(ctx); err != nil {
		return nil, err
	}
	return config, nil
}

func (config *custodyConfigV1) check(ctx context.Context) error {
	if config == nil || config.manager == nil || config.root == nil || config.lock == nil {
		return sessionLeaseError("the owned custody configuration scope ended")
	}
	if err := errors.Join(ctx.Err(), config.manager.checkNativeManager(), config.rootRef.CheckRoot(config.root), config.lockRef.CheckFile(config.manager.root, brand.Name+"-custody-config.lock", config.lock, 0o600)); err != nil {
		return err
	}
	// Recheck the path from the SAME runtime root, not from a recovered name.
	named, err := config.manager.root.OpenRoot("systemd/user")
	if err != nil {
		return err
	}
	return errors.Join(config.rootRef.CheckRoot(named), named.Close())
}

func (config *custodyConfigV1) reload(ctx context.Context) error {
	if err := config.check(ctx); err != nil {
		return err
	}
	err := config.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".Reload", 0).Err
	return errors.Join(err, config.check(ctx))
}

func (config *custodyConfigV1) Close() error {
	if config == nil {
		return nil
	}
	if config.manager == nil {
		return sessionLeaseError("the original config resource manager is absent")
	}
	pool := &config.manager.retirement
	fileErr := pool.closeFilesV4([]*os.File{config.lock})
	rootErr := pool.closeRootsV4([]*os.Root{config.root})
	return errors.Join(fileErr, rootErr, pool.checkFilesV4([]*os.File{config.lock}), pool.checkRootsV4([]*os.Root{config.root}))
}

type custodyConfigFileV1 struct {
	root       *os.Root
	name       string
	file       *os.File
	ref        cachefs.Reference
	content    []byte
	created    bool
	incomplete bool
	closeFacts *supply.FileCloseFactsV1
}

func openCustodyConfigFileV1(root *os.Root, name string, content []byte, create bool) (result *custodyConfigFileV1, failure error) {
	flags := os.O_RDONLY
	if create {
		flags = os.O_RDWR | os.O_CREATE | os.O_EXCL
	}
	file, err := cachefs.OpenFile(root, name, flags, 0o600)
	if err != nil {
		return nil, err
	}
	owned := &custodyConfigFileV1{root: root, name: name, file: file, content: bytes.Clone(content), created: create, incomplete: create}
	defer func() {
		if failure != nil {
			if create {
				cleanup := owned.removeCreatedV1()
				failure = errors.Join(failure, cleanup)
				if cleanup != nil {
					result = owned
					return
				}
			}
			cleanup := owned.closeDescriptorV1()
			failure = errors.Join(failure, cleanup)
			if owned.closeFacts == nil || !owned.closeFacts.Retired || owned.closeFacts.Unknown != nil {
				result = owned
			}
		}
	}()
	if create {
		n, err := file.Write(content)
		if err != nil || n != len(content) {
			return nil, errors.Join(err, io.ErrShortWrite)
		}
		if err := file.Sync(); err != nil {
			return nil, err
		}
	}
	owned.ref, err = cachefs.CaptureFile(root, name, file, 0o600)
	if err != nil {
		return nil, err
	}
	if err := owned.check(); err != nil {
		return nil, err
	}
	owned.incomplete = false
	return owned, nil
}

func (file *custodyConfigFileV1) check() error {
	if file == nil || file.file == nil {
		return sessionLeaseError("the original custody configuration file ended")
	}
	if err := file.ref.CheckFile(file.root, file.name, file.file, 0o600); err != nil {
		return err
	}
	data := make([]byte, len(file.content)+1)
	n, err := file.file.ReadAt(data, 0)
	if err != nil && err != io.EOF || !bytes.Equal(data[:n], file.content) {
		return errors.Join(sessionLeaseError("the owned custody configuration changed its exact contents"), err)
	}
	return nil
}

func (file *custodyConfigFileV1) remove() error {
	if err := file.check(); err != nil {
		return err
	}
	return file.root.Remove(file.name)
}

func ensureCustodyConfigFileV1(root *os.Root, name string, content []byte) (*custodyConfigFileV1, error) {
	file, err := openCustodyConfigFileV1(root, name, content, true)
	if !errors.Is(err, os.ErrExist) {
		return file, err
	}
	return openCustodyConfigFileV1(root, name, content, false)
}
