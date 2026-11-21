package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// prepareFileCapture is called only by the process adapter, before launch.
func prepareFileCapture(m *host.Manifest) (*files.Capture, error) {
	if m.Files == nil || len(m.Files.State.Files) == 0 {
		return nil, nil
	}
	capture, err := files.BeginCapture(paths.SessionDir(m.ID), m.Files.State)
	if err != nil {
		return capture, wallError("cannot prepare agent state", err)
	}
	return capture, nil
}

func (s *Supervisor) adoptFileCapture(sb sandbox) {
	if runtime, ok := sb.(interface{ StateCapture() *files.Capture }); ok {
		s.capture = runtime.StateCapture()
	}
}

func (s *Supervisor) seedWorkspace(ctx context.Context, m *host.Manifest) error {
	if m.Files == nil {
		return nil
	}
	for _, mount := range m.Files.Mounts {
		s.audit.write("mount", map[string]any{"guest": mount.Guest, "write": mount.Write})
	}
	seed := m.Files.Seed
	if seed.Laptop {
		return nil
	}
	args := proto.CtlSeedArgs{Token: s.id, Repo: seed.Repo, Commit: seed.Commit, Branch: seed.Branch, SourceBranch: seed.SourceBranch, Origin: seed.Origin, Size: seed.BundleSize, Scratch: seed.Scratch}
	for _, f := range seed.Includes {
		args.Includes = append(args.Includes, proto.SeedFile{Path: f.Path, Size: f.Size})
	}
	var result proto.CtlSeedResult
	if err := s.ctl.request(ctx, proto.CtlSeed, args, &result); err != nil {
		return err
	}
	if !result.Needed {
		return nil
	}
	stream, err := s.link.Open(ctx, link.KindBundle, map[string]string{"token": s.id})
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	err = s.sendSeed(stream, seed)
	if err != nil {
		_ = stream.Close()
		return wallError("cannot send seed", err)
	}
	if err = stream.CloseWrite(); err != nil {
		_ = stream.Close()
		return err
	}
	args.Apply = true
	err = s.ctl.request(ctx, proto.CtlSeed, args, nil)
	_ = stream.Close()
	return err
}

func (s *Supervisor) sendSeed(out io.Writer, seed host.Seed) error {
	root, err := os.OpenRoot(paths.SessionDir(s.id))
	if err != nil {
		return err
	}
	defer root.Close()
	send := func(root *os.Root, name string, size int64) error {
		f, err := files.OpenRegular(root, name)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if st.Size() != size {
			return fmt.Errorf("seed file size changed")
		}
		_, err = io.CopyN(out, f, size)
		return err
	}
	if err = send(root, "seed.bundle", seed.BundleSize); err != nil {
		return err
	}
	includes, err := root.OpenRoot("seed-includes")
	if err != nil {
		return err
	}
	defer includes.Close()
	for _, f := range seed.Includes {
		if err = send(includes, f.Path, f.Size); err != nil {
			return err
		}
		s.audit.write("file_exposure", map[string]any{"path": f.Path, "source": "include"})
	}
	return nil
}

// readyCapture only accepts this run's nonce. A guest cannot name a host
// path or publish CURRENT. The actual exit and cleanup are checked later.
func (s *Supervisor) readyCapture(nonce string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capture != nil && nonce == s.capture.Nonce || s.vmCaptureReadyLocked(nonce) {
		s.captureReady = nonce
	}
}

func (s *Supervisor) captureWillClose() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture != nil && s.captureReady == s.capture.Nonce || s.vmCaptureReadyLocked(s.captureReady)
}

func (s *Supervisor) commitCapture(kind string, natural bool, cleanupErrors ...error) (result error) {
	if s.capture == nil {
		return
	}
	defer func() { result = errors.Join(result, s.capture.Close()) }()
	s.mu.Lock()
	nonce := s.captureReady
	s.mu.Unlock()
	if !s.captureEndEligible(kind, natural, errors.Join(cleanupErrors...)) || nonce == "" {
		return
	}
	if err := s.capture.Commit(true, nonce); err != nil {
		s.log.Error("cannot publish agent state", "err", err)
		s.audit.write("state_capture_failed", nil)
		return err
	}
	s.audit.write("state_saved", map[string]any{"at": time.Now().UTC()})
	return nil
}

// captureEndEligible is shared by process capture and the VM's pending-state
// acceptance. Only the actual driver's owned clean exit can satisfy it.
func (s *Supervisor) captureEndEligible(kind string, natural bool, cleanupErr error) bool {
	if cleanupErr != nil {
		return false
	}
	runtime, ok := s.sb.(interface{ CleanExit() bool })
	if !ok || !runtime.CleanExit() {
		return false
	}
	s.mu.Lock()
	s.settleKills()
	noBreach := s.lost == "" && !s.silent && !s.captureFailed && !s.rec.oom && !s.rec.oomAgent
	versioned := s.generation != ""
	s.mu.Unlock()
	if !noBreach || s.link != nil && errors.Is(s.link.Err(), link.ErrBreach) {
		return false
	}
	switch kind {
	case host.EndStop, host.EndSystem:
		// A stopped legacy holder need not emit a natural agent-end event.
		return true
	case host.EndSandbox, host.EndEmpty:
		// Keep the P16 unversioned capture contract. Every current controller
		// registers a generation and must correlate all launched members.
		return natural && (!versioned || s.correlatedEmpty())
	default:
		return false
	}
}

func (s *Supervisor) validCaptureMessage(p []byte) bool {
	claim, ok := parseEvent(p, s.id, "")
	if !ok || claim.event != proto.EventStateReady {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture != nil && claim.captureNonce == s.capture.Nonce || s.vmCaptureReadyLocked(claim.captureNonce)
}
