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
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

type runtimeStorage struct {
	session         *os.Root
	root            *os.Root
	lease           *os.File
	sessionIdentity os.FileInfo
	identity        os.FileInfo
	dir             string
	staged          string
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
			returned = errors.Join(returned, owned.release())
		}
	}()
	f, err := session.Open(".")
	if err != nil {
		return nil, err
	}
	storage.sessionIdentity, err = f.Stat()
	f.Close()
	if err != nil {
		return nil, err
	}
	if err = storage.lock(ctx); err != nil {
		return nil, err
	}
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
		closeErr := f.Close()
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
		if err = errors.Join(writeErr, f.Close()); err != nil {
			return nil, err
		}
	}
	return storage, nil
}

func (s *runtimeStorage) lock(ctx context.Context) error {
	lease, err := cachefs.OpenFile(s.session, "gvisor.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			lease.Close()
			return err
		}
		got, err := cachefs.TryLock(lease, cachefs.Exclusive)
		if err != nil {
			lease.Close()
			return err
		}
		if got {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			lease.Close()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if _, err := cachefs.ValidateFile(s.session, "gvisor.lock", lease, 0600); err != nil {
		lease.Close()
		return err
	}
	s.lease = lease
	return nil
}

func (s *runtimeStorage) bundle(spec wall.Spec, plan []wall.Mount, smoke bool) error {
	oci, err := makeOCI(spec, plan, smoke)
	if err != nil {
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
			info, err := os.Lstat(mount.Source)
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
	closeErr := f.Close()
	if err = errors.Join(encodeErr, closeErr); err != nil {
		return err
	}
	return bundle.Rename("config.json.new", "config.json")
}

func (s *runtimeStorage) log() (*os.File, error) {
	return cachefs.OpenFile(s.root, "runsc.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

func (s *runtimeStorage) release() error {
	var result error
	if s.root != nil {
		result = errors.Join(result, s.root.Close())
		s.root = nil
	}
	if s.lease != nil {
		result = errors.Join(result, s.lease.Close())
		s.lease = nil
	}
	if s.session != nil {
		result = errors.Join(result, s.session.Close())
		s.session = nil
	}
	return result
}

func (s *runtimeStorage) reopen(ctx context.Context) error {
	if s.session != nil {
		return nil
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
	closeErr := f.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(got, s.sessionIdentity) {
		return errors.Join(fmt.Errorf("gvisor session root changed after release"), statErr, closeErr, s.release())
	}
	if err := s.lock(ctx); err != nil {
		return errors.Join(err, s.release())
	}
	return nil
}

func (s *runtimeStorage) destroy() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.reopen(ctx); err != nil {
		return err
	}
	dir, err := s.session.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
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
	return s.release()
}
