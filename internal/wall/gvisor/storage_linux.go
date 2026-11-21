//go:build linux

package gvisor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

type runtimeStorage struct {
	home            *freshForkHomeV1
	session         *os.Root
	root            *os.Root
	lease           *os.File
	sessionIdentity os.FileInfo
	identity        os.FileInfo
	dir             string
	staged          string
	closeErr        error
	failedFiles     []*os.File
	layoutStarted   bool
	removed         bool
}

func prepareStorage(ctx context.Context, spec wall.Spec, existing bool) (storage *runtimeStorage, returned error) {
	session, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return nil, err
	}
	storage = &runtimeStorage{session: session, dir: spec.SessionDir}
	owned := storage
	defer func() {
		if returned != nil {
			storage = owned
		}
	}()
	f, err := session.Open(".")
	if err != nil {
		return nil, err
	}
	storage.sessionIdentity, err = f.Stat()
	closeErr := storage.closeFileV1(f)
	err = errors.Join(err, closeErr)
	if err != nil {
		return nil, err
	}
	if err = storage.lock(ctx); err != nil {
		return nil, err
	}
	storage.layoutStarted = true
	if !existing {
		if err = cachefs.MakePrivateDir(session, "gvisor"); err != nil {
			return nil, err
		}
	}
	info, err := session.Lstat("gvisor")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("gvisor runtime is not an owned directory"), err)
	}
	storage.identity = info
	storage.root, err = session.OpenRoot("gvisor")
	if err != nil {
		return nil, err
	}
	if existing {
		f, err := cachefs.OpenFile(storage.root, "session", os.O_RDONLY, 0600)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, int64(len(spec.ID)+2)))
		closeErr := storage.closeFileV1(f)
		if readErr != nil || closeErr != nil || string(data) != spec.ID+"\n" {
			return nil, errors.Join(fmt.Errorf("gvisor runtime belongs to another session"), readErr, closeErr)
		}
	} else {
		f, err := cachefs.OpenFile(storage.root, "session", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		n, writeErr := io.WriteString(f, spec.ID+"\n")
		if writeErr == nil && n != len(spec.ID)+1 {
			writeErr = io.ErrShortWrite
		}
		if err = errors.Join(writeErr, storage.closeFileV1(f)); err != nil {
			return nil, err
		}
	}
	storage.home, err = createFreshForkHomeV1(storage)
	if err != nil {
		return nil, err
	}
	return storage, nil
}

func (s *runtimeStorage) lock(ctx context.Context) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	lease := s.lease
	if lease == nil {
		var err error
		lease, err = cachefs.OpenFile(s.session, "gvisor.lock", os.O_CREATE|os.O_RDWR, 0o600)
		s.lease = lease // Retain even an actual partial open or failed lock.
		if err != nil {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		got, err := cachefs.TryLock(lease, cachefs.Exclusive)
		if err != nil {
			return err
		}
		if got {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	_, err := cachefs.ValidateFile(s.session, "gvisor.lock", lease, 0o600)
	return err
}

func (s *runtimeStorage) bundle(spec wall.Spec, plan []wall.Mount, sources []*os.File, smoke bool, fork *ForkMountsV1) error {
	if len(plan) != len(sources) {
		return fmt.Errorf("the gvisor bundle lost its captured mount vector")
	}
	captured := make(map[string]*os.File, len(plan))
	for index, mount := range plan {
		file := sources[index]
		if file == nil {
			return fmt.Errorf("the gvisor bundle has no captured mount capability")
		}
		identity, err := files.Identity(file)
		if err != nil || identity != mount.Identity || identity == "" {
			return errors.Join(fmt.Errorf("the gvisor bundle mount differs from its captured source"), err)
		}
		if old := captured[mount.Host]; old != nil {
			oldIdentity, err := files.Identity(old)
			if err != nil || oldIdentity != identity {
				return errors.Join(fmt.Errorf("the gvisor mount path has conflicting captured identities"), err)
			}
		}
		captured[mount.Host] = file
	}
	oci, err := makeOCI(spec, plan, smoke)
	if err != nil {
		return err
	}
	if fork == nil || fork.home != s.home || fork.sandbox == nil || fork.sandbox.storage != s {
		return failure("wall_setup", "the actual OCI has no original home and mount factory", nil)
	}
	if err := fork.routeOCI(&oci); err != nil {
		return err
	}
	for _, dir := range []string{"bundle", "runsc"} {
		if err := cachefs.MakePrivateDir(s.root, dir); err != nil {
			return err
		}
	}
	bundle, err := s.root.OpenRoot("bundle")
	if err != nil {
		return err
	}
	defer bundle.Close()
	if err := cachefs.MakePrivateDir(bundle, "rootfs"); err != nil {
		return err
	}
	root, err := bundle.OpenRoot("rootfs")
	if err != nil {
		return err
	}
	defer root.Close()
	for _, mount := range oci.Mounts {
		name := strings.TrimPrefix(mount.Destination, "/")
		directory := true
		if mount.Type == "bind" {
			source := captured[mount.Source]
			if mount.Destination == "/home/agent" || fork.routes[mount.Destination] != "" {
				// Fixed A aliases originate in this SAME private producer; names
				// route mount data only and never reopen a source as authority.
				if mount.Destination == "/home/agent" && mount.Source == "/home/agent" {
					source = fork.home.file
				} else if mount.Source == "/work" {
					source = fork.work
				} else if strings.HasPrefix(mount.Source, "/home/agent/") {
					source = fork.state[strings.TrimPrefix(mount.Source, "/home/agent/")]
				}
			}
			if source == nil {
				return fmt.Errorf("the gvisor bundle references an uncaptured mount source")
			}
			info, err := source.Stat()
			if err != nil {
				return err
			}
			directory = info.IsDir()
			if !directory && !info.Mode().IsRegular() {
				return fmt.Errorf("gvisor source is not a regular file or directory")
			}
		}
		if directory {
			err = root.MkdirAll(name, 0755)
		} else {
			err = root.MkdirAll(filepath.Dir(name), 0755)
			if err == nil {
				f, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
				err = e
				if errors.Is(err, os.ErrExist) {
					info, e := root.Lstat(name)
					if e != nil || !info.Mode().IsRegular() {
						return errors.Join(fmt.Errorf("gvisor destination changed type"), e)
					}
					err = nil
				}
				if f != nil {
					err = errors.Join(err, f.Close())
				}
			}
		}
		if err != nil {
			return err
		}
	}
	f, err := cachefs.OpenFile(bundle, "config.json.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(f).Encode(oci)
	closeErr := s.closeFileV1(f)
	if err = errors.Join(encodeErr, closeErr); err != nil {
		return err
	}
	return bundle.Rename("config.json.new", "config.json")
}

// commandBindsV1 opens beneath the original retained runtime root. Path text
// names the fixed targets only; these same capabilities supply the mounts.
func (s *runtimeStorage) commandBindsV1() (binds []jail.Bind, returned error) {
	defer func() {
		if returned != nil {
			for _, bound := range binds {
				returned = errors.Join(returned, bound.File.Close())
			}
			binds = nil
		}
	}()
	for _, name := range []string{"bundle", "runsc"} {
		file, err := s.root.Open(name)
		if err != nil {
			return binds, err
		}
		binds = append(binds, jail.Bind{Path: filepath.Join(s.dir, "gvisor", name), File: file, Write: name == "runsc"})
		info, err := file.Stat()
		if err != nil || !info.IsDir() {
			return binds, errors.Join(fmt.Errorf("the gvisor control root is not its owned directory"), err)
		}
	}
	return binds, nil
}

func (s *runtimeStorage) log() (*os.File, error) {
	return cachefs.OpenFile(s.root, "runsc.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

func (s *runtimeStorage) release() error {
	if s == nil {
		return nil
	}
	if s.closeErr != nil {
		return s.closeErr
	}
	if s.home != nil {
		if err := s.home.close(); err != nil {
			return err // Retain the same original fresh-home owner for retry.
		}
	}
	if s.root != nil {
		if err := s.root.Close(); err != nil {
			s.closeErr = errors.Join(s.closeErr, err)
		} else {
			s.root = nil
		}
	}
	if s.lease != nil {
		if err := s.lease.Close(); err != nil {
			s.closeErr = errors.Join(s.closeErr, err)
		} else {
			s.lease = nil
		}
	}
	if s.session != nil {
		if err := s.session.Close(); err != nil {
			s.closeErr = errors.Join(s.closeErr, err)
		} else {
			s.session = nil
		}
	}
	return s.closeErr
}

func (s *runtimeStorage) closeFileV1(file *os.File) error {
	if err := file.Close(); err != nil {
		s.failedFiles = append(s.failedFiles, file)
		s.closeErr = errors.Join(s.closeErr, err)
		return err
	}
	return nil
}

func (s *runtimeStorage) reopen(ctx context.Context) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	if s.session != nil {
		return s.lock(ctx)
	}
	if s.sessionIdentity == nil {
		return fmt.Errorf("gvisor has no original session identity for reopening")
	}
	root, err := cachefs.OpenPrivateRoot(s.dir, false)
	if err != nil {
		return err
	}
	s.session = root
	f, err := root.Open(".")
	if err != nil {
		return errors.Join(err, s.release())
	}
	got, statErr := f.Stat()
	closeErr := s.closeFileV1(f)
	if statErr != nil || closeErr != nil || !os.SameFile(got, s.sessionIdentity) {
		return errors.Join(fmt.Errorf("gvisor session root changed after release"), statErr, closeErr, s.release())
	}
	if err := s.lock(ctx); err != nil {
		return errors.Join(err, s.release())
	}
	return nil
}

func (s *runtimeStorage) destroy() (returned error) {
	if s == nil {
		return nil
	}
	if s.closeErr != nil {
		return s.closeErr
	}
	if s.removed || !s.layoutStarted {
		return s.release()
	}
	if s.identity == nil {
		return fmt.Errorf("the partial gvisor layout has no original native identity for removal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.reopen(ctx); err != nil {
		return err
	}
	if s.home != nil && !s.home.closed {
		if err := s.home.close(); err != nil {
			return err
		}
	}
	dir, err := s.session.Open(".")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, s.closeFileV1(dir)) }()
	name := s.staged
	if name == "" {
		name = ".gvisor-cleanup-" + rand.Text()
		if err := unix.Renameat2(int(dir.Fd()), "gvisor", int(dir.Fd()), name, unix.RENAME_NOREPLACE); err != nil {
			return err
		}
		s.staged = name
	}
	got, err := s.session.Lstat(name)
	if err != nil || !os.SameFile(s.identity, got) {
		restoreErr := unix.Renameat2(int(dir.Fd()), name, int(dir.Fd()), "gvisor", unix.RENAME_NOREPLACE)
		if restoreErr == nil {
			s.staged = ""
		}
		return errors.Join(fmt.Errorf("gvisor runtime object changed before destruction"), err, restoreErr)
	}
	if err := s.session.RemoveAll(name); err != nil {
		return err
	}
	s.staged = ""
	s.removed = true
	return s.release()
}
