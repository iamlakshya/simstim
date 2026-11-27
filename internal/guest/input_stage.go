package guest

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

type inputStage struct {
	parent, payload *os.Root
	name            string
	removed         bool
}

func (g *Init) newInputStage(nonce api.InputNonce) (_ *inputStage, err error) {
	if !nonce.Valid() {
		return nil, inputError("invalid input staging nonce")
	}
	run := g.path(host.GuestRunDir())
	if err := os.MkdirAll(run, 0o755); err != nil {
		return nil, err
	}
	info, err := os.Lstat(run)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !inputOwner(info, os.Geteuid()) {
		return nil, inputError("the input runtime root is not owned by init")
	}
	if !inputOwner(info, AgentUID) && info.Mode().Perm()&0o001 == 0 {
		return nil, inputError("the input runtime root does not allow fixed-account traversal")
	}
	runtime, err := os.OpenRoot(run)
	if err != nil {
		return nil, err
	}
	var stage *inputStage
	defer func() {
		if err != nil && stage != nil {
			err = errors.Join(err, stage.close())
		}
	}()
	defer func() { err = errors.Join(err, runtime.Close()) }()
	if err = runtime.Mkdir("inputs", 0o711); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = runtime.Lstat("inputs")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !inputOwner(info, os.Geteuid()) || info.Mode().Perm() != 0o711 {
		return nil, inputError("the protected input parent changed")
	}
	parent, err := runtime.OpenRoot("inputs")
	if err != nil {
		return nil, err
	}
	stage = &inputStage{parent: parent, name: string(nonce)}
	if err = parent.Mkdir(stage.name, 0o700); err != nil {
		return nil, err
	}
	payload, err := parent.OpenRoot(stage.name)
	if err != nil {
		return nil, err
	}
	stage.payload = payload
	if err = payload.Chown(".", AgentUID, AgentGID); err != nil {
		return nil, err
	}
	return stage, nil
}

func (s *inputStage) cleanup() error {
	if s.removed {
		return nil
	}
	if s.payload == nil {
		return inputError("the input staging root was not pinned")
	}
	want, err := s.payload.Stat(".")
	if err != nil {
		return err
	}
	got, err := s.parent.Lstat(s.name)
	if err != nil || !got.IsDir() || !os.SameFile(want, got) {
		return inputError("the input staging identity changed before cleanup")
	}
	if err = s.parent.RemoveAll(s.name); err != nil {
		return err
	}
	s.removed = true
	return nil
}
func (s *inputStage) close() error {
	err := s.cleanup()
	if s.payload != nil {
		err = errors.Join(err, s.payload.Close())
		s.payload = nil
	}
	if s.parent != nil {
		err = errors.Join(err, s.parent.Close())
		s.parent = nil
	}
	return err
}

func inputCommandEnv() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + host.GuestHome, "XDG_CONFIG_HOME=" + filepath.Join(host.GuestHome, ".config"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C.UTF-8"}
}
