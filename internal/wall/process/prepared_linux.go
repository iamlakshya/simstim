//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/wall"
)

// ReleasePreparedV1 releases only the actual factory's metadata handles. It
// fences every later Start and refuses a running or unproven namespace init.
func (s *sandbox) ReleasePreparedV1() error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	if s.cmd != nil && (s.pidfd >= 0 || !s.closed || s.creator == nil) {
		s.mu.Unlock()
		return setupError("the live or unproven process init owns its prepared resources", nil)
	}
	creator, started := s.creator, s.cmd != nil
	s.preparedReleased = true
	s.mu.Unlock()
	if started {
		if ready, err := creator.check(); err != nil || !ready {
			return setupError("the original process creator has no genuine prepared-release closure", err)
		}
	}
	if err := s.closeUnstartedCreatorV3(); err != nil {
		return err
	}
	if s.copy != nil {
		if err := s.copy.native.nativeExit(); err != nil {
			return err
		}
	}
	if s.freezer != nil {
		if err := s.freezer.closeEmpty(); err != nil {
			return err
		}
	}
	return s.storage.release()
}

// The resource exists before the first file mutation. Every late error
// returns that same genuine owner, never a cleanup-success surrogate.
func prepareOwnedProcessSpecV1(resource wall.Sandbox, spec wall.Spec, body []byte) (wall.Sandbox, error) {
	s, ok := resource.(*sandbox)
	if !ok || s.storage == nil || s.storage.root == nil {
		return resource, setupError("the process specification has no actual factory owner", nil)
	}
	fail := func(message string, err error) (wall.Sandbox, error) {
		s.preparationErr = setupError(message, err)
		return s, s.preparationErr
	}
	root := s.storage.root
	file, err := root.OpenFile("process.json.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fail("cannot write sandbox specification", err)
	}
	created, err := file.Stat()
	if err != nil {
		closeErr := s.closePreparedFileV1(file)
		return fail("cannot identify new sandbox specification", errors.Join(err, closeErr))
	}
	s.storage.owned["process.json.new"] = created
	if err := writePreparedSpec(&processPreparedFileV1{file: file, owner: s}, body); err != nil {
		return fail("cannot save sandbox specification", err)
	}
	if err := root.Rename("process.json.new", "process.json"); err != nil {
		return fail("cannot publish sandbox specification", err)
	}
	delete(s.storage.owned, "process.json.new")
	s.storage.owned["process.json"] = created
	return s, nil
}

type processPreparedFileV1 struct {
	file  *os.File
	owner *sandbox
}

func (file *processPreparedFileV1) Write(body []byte) (int, error) { return file.file.Write(body) }
func (file *processPreparedFileV1) Sync() error                    { return file.file.Sync() }
func (file *processPreparedFileV1) Close() error                   { return file.owner.closePreparedFileV1(file.file) }
func (s *sandbox) closePreparedFileV1(file *os.File) error {
	if err := file.Close(); err != nil {
		s.storage.failedFiles = append(s.storage.failedFiles, file)
		s.storage.closeErr = errors.Join(s.storage.closeErr, fmt.Errorf("close actual prepared process file: %w", err))
		return err
	}
	return nil
}
