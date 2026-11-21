//go:build linux

package process

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

type sandbox struct {
	spec                wall.Spec
	lifeMu              sync.Mutex
	mu                  sync.Mutex
	storage             *runtimeStorage
	cmd                 *exec.Cmd
	pidfd               int
	copy                *CopyOwnerV1
	link                *net.UnixConn
	exited              chan struct{}
	closed              bool
	preparedReleased    bool
	preparationErr      error
	forced              bool
	initProven          bool
	creator             *processCreatorClosureV3
	partialCreator      *os.File
	partialCreatorFacts *supply.FileCloseFactsV1
	result              wall.Exit
	transferDone        chan struct{}
	freezer             *processFreezerV1
	freezerOff          string
}

func newSandbox(spec wall.Spec) (wall.Sandbox, error) {
	storage, err := openRuntimeStorage(spec.SessionDir)
	if storage == nil {
		return nil, setupError("cannot pin sandbox runtime storage", err)
	}
	resource := &sandbox{spec: spec, storage: storage, pidfd: -1, exited: make(chan struct{}), preparationErr: err}
	if err != nil {
		return resource, setupError("cannot pin sandbox runtime storage", err)
	}
	return resource, nil
}

// Start owns every descriptor and partial child before returning. The caller's
// context bounds the start, while the sandbox outlives a successful start.
func (s *sandbox) Start(ctx context.Context) (_ net.Conn, returned error) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	unavailable := s.closed || s.cmd != nil || s.preparedReleased || s.preparationErr != nil || s.freezer != nil
	s.mu.Unlock()
	if unavailable {
		return nil, setupError("sandbox already started or closed", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, setupError("sandbox start was cancelled", err)
	}
	plan, err := MountPlan(s.spec)
	if err != nil {
		return nil, setupError("cannot plan process mounts", err)
	}
	launcher, err := launcherPath(s.spec.Helper)
	if err != nil {
		return nil, err
	}
	rootPath := filepath.Join(s.spec.SessionDir, "process-root")
	if err = s.storage.root.Mkdir("process-root", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, setupError("cannot make private root mount point", err)
	}
	if err = s.storage.capture("process-root"); err != nil {
		return nil, setupError("cannot identify private root mount point", err)
	}
	root, err := pinSource(rootPath)
	if err != nil {
		return nil, setupError("cannot pin private root mount point", err)
	}
	defer root.Close()
	rootInfo, err := root.Stat()
	if err != nil || !os.SameFile(s.storage.owned["process-root"], rootInfo) {
		return nil, setupError("private root mount point changed", err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, setupError("cannot make guest link", err)
	}
	ours := os.NewFile(uintptr(fds[0]), "supervisor-link")
	theirs := os.NewFile(uintptr(fds[1]), "guest-link")
	defer theirs.Close()
	conn, err := net.FileConn(ours)
	ours.Close()
	if err != nil {
		return nil, setupError("cannot adopt guest link", err)
	}
	link, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, setupError("guest link is not a Unix stream", nil)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		link.Close()
		return nil, setupError("cannot make launcher request pipe", err)
	}
	defer reader.Close()
	defer writer.Close()
	copyOwner, copyChild, err := copyControlV1(s)
	if copyOwner != nil {
		s.mu.Lock()
		s.copy = copyOwner
		s.mu.Unlock()
	}
	if err != nil {
		link.Close()
		return nil, setupError("cannot create original copy controller", err)
	}
	defer copyChild.Close()
	startedCopy := false
	defer func() {
		if !startedCopy {
			returned = errors.Join(returned, copyOwner.native.nativeExit())
		}
	}()
	completion, completionWriter, err := os.Pipe()
	if err != nil {
		link.Close()
		return nil, setupError("make original native creator completion pipe", err)
	}
	defer completionWriter.Close()
	creatorTransferred := false
	defer func() {
		if !creatorTransferred {
			_ = completion.Close()
		}
	}()
	completionFD := int(completion.Fd())
	if err := unix.SetNonblock(completionFD, true); err != nil {
		link.Close()
		return nil, setupError("retain nonblocking original creator completion", err)
	}
	extra := []*os.File{reader, theirs, root, copyChild, completionWriter}
	req := Request{V: RequestVersion, Mounts: []MountRequest{}}
	factoryStages := map[string]*copyMountNativeV1{}
	placement, placementErr := copyPlacementV3(s.spec.SessionDir, plan)
	if placementErr != nil {
		link.Close()
		return nil, setupError("cannot exclude private copy storage from guest sources", placementErr)
	}
	for _, m := range plan {
		var f *os.File
		var e error
		if m.Identity != "" {
			f, e = files.PinGrant(m)
		} else {
			f, e = pinSource(m.Host)
		}
		if e != nil {
			for _, f := range extra[4:] {
				f.Close()
			}
			link.Close()
			return nil, setupError("cannot pin sandbox mount", e)
		}
		extra = append(extra, f)
		record := MountRequest{FD: len(extra) + 2, Guest: m.Guest, Write: m.Write}
		info, inspectErr := f.Stat()
		if inspectErr != nil {
			for _, file := range extra[4:] {
				_ = file.Close()
			}
			link.Close()
			return nil, setupError("inspect original copy mount", inspectErr)
		}
		if placement && m.Write && info.IsDir() {
			stage, stageExtra, stageErr := prepareCopyMountV3(s.storage.root, f)
			if errors.Is(stageErr, unix.EXDEV) {
				req.Mounts = append(req.Mounts, record)
				continue
			}
			if stageErr != nil {
				for _, file := range extra[4:] {
					_ = file.Close()
				}
				link.Close()
				return nil, setupError("prepare original factory staging", stageErr)
			}
			factoryStages[m.Guest] = stage
			copyOwner.native.mounts[uint32(len(req.Mounts))] = stage
			extra = append(extra, stageExtra)
			record.StageFD = len(extra) + 2
			record.StageName = stage.ownedName
		}
		req.Mounts = append(req.Mounts, record)
	}
	sort.Slice(req.Mounts, func(i, j int) bool { return req.Mounts[i].Guest < req.Mounts[j].Guest })
	copyOwner.native.mounts = make(map[uint32]*copyMountNativeV1)
	for i, mount := range req.Mounts {
		if stage := factoryStages[mount.Guest]; stage != nil {
			copyOwner.native.mounts[uint32(i)] = stage
		}
	}
	defer func() {
		for _, f := range extra[4:] {
			f.Close()
		}
	}()
	if err = s.storage.capture("namespace.log"); err != nil {
		link.Close()
		return nil, setupError("cannot identify namespace log", err)
	}
	logf, err := s.storage.root.OpenFile("namespace.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		link.Close()
		return nil, setupError("cannot open namespace log", err)
	}
	defer logf.Close()
	if err = s.storage.capture("namespace.log"); err != nil {
		link.Close()
		return nil, setupError("cannot own namespace log", err)
	}
	logInfo, err := logf.Stat()
	if err != nil || !os.SameFile(logInfo, s.storage.owned["namespace.log"]) {
		link.Close()
		return nil, setupError("namespace log changed before use", err)
	}
	freezer, freezeErr := prepareProcessFreezerV1(ctx)
	s.mu.Lock()
	s.freezer = freezer
	if freezeErr != nil {
		s.freezerOff = freezeErr.Error()
	}
	s.mu.Unlock()
	if freezeErr != nil && freezer != nil {
		link.Close()
		return nil, setupError("retain the original guest cgroup until its cleanup is proved", freezeErr)
	}
	freezerStarted := false
	defer func() {
		if !freezerStarted && freezer != nil {
			returned = errors.Join(returned, freezer.closeEmpty())
		}
	}()
	cmd := exec.Command(launcher)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = extra
	cmd.Stdout, cmd.Stderr = logf, logf
	creatorPIDFD := -1
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM, PidFD: &creatorPIDFD}
	var nativeFreezer *processCgroupV1
	if freezer != nil {
		nativeFreezer = freezer.control.(*processCgroupV1)
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(nativeFreezer.child.Fd())
	}
	s.mu.Lock()
	if s.closed || s.cmd != nil {
		s.mu.Unlock()
		link.Close()
		return nil, setupError("sandbox already started or closed", nil)
	}
	if err = ctx.Err(); err != nil {
		s.mu.Unlock()
		link.Close()
		return nil, setupError("sandbox start was cancelled", err)
	}
	if nativeFreezer != nil {
		if err = nativeFreezer.check(false); err != nil {
			s.mu.Unlock()
			link.Close()
			return nil, setupError("the original guest cgroup changed before launcher creation", err)
		}
	}
	if err = cmd.Start(); err != nil {
		if creatorPIDFD >= 0 {
			s.partialCreator = os.NewFile(uintptr(creatorPIDFD), "actual-unstarted-process-creator")
		}
		s.preparationErr = setupError("the original native process start failed before request delivery", err)
		s.mu.Unlock()
		link.Close()
		return nil, setupError("cannot start static namespace launcher", err)
	}
	if nativeFreezer != nil {
		freezer.mu.Lock()
		duplicate, duplicateErr := unix.FcntlInt(uintptr(creatorPIDFD), unix.F_DUPFD_CLOEXEC, 0)
		freezeErr = duplicateErr
		if duplicateErr == nil {
			freezeErr = nativeFreezer.bindCreatedLauncherV1(duplicate, cmd.Process.Pid)
		}
		freezer.mu.Unlock()
	}
	s.cmd, s.link, s.transferDone = cmd, link, make(chan struct{})
	s.creator = &processCreatorClosureV3{completion: completion, completionFD: completionFD, pidfd: creatorPIDFD}
	creatorTransferred = true
	s.copy = copyOwner
	startedCopy = true
	freezerStarted = true
	// After spawn only the original C creator keeps this writer. The child
	// closes its inherited alias before any setup; Go never writes a receipt.
	s.result.Err = errors.Join(s.result.Err, completionWriter.Close())
	s.mu.Unlock()
	go s.reap(cmd)
	var transferOnce sync.Once
	finishTransfer := func() { transferOnce.Do(func() { close(s.transferDone) }) }
	defer finishTransfer()
	requestDone := make(chan error, 1)
	go func() {
		e := json.NewEncoder(writer).Encode(req)
		closeErr := writer.Close()
		if e == nil {
			e = closeErr
		}
		requestDone <- e
	}()
	abort := func(cause error) (net.Conn, error) {
		writer.Close()
		link.Close()
		copyOwner.native.interrupt()
		killErr := s.Kill()
		finishTransfer()
		result := s.Wait()
		return nil, setupError("namespace launcher did not start the sandbox", errors.Join(cause, killErr, result.Err))
	}
	if freezeErr != nil && freezer != nil {
		return abort(freezeErr)
	}
	select {
	case err = <-requestDone:
		if err != nil {
			return abort(err)
		}
	case <-ctx.Done():
		return abort(ctx.Err())
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = link.SetReadDeadline(deadline)
	}
	received := make(chan error, 1)
	go func() { received <- s.adoptInit(link) }()
	select {
	case err = <-received:
		if err != nil {
			return abort(err)
		}
	case <-ctx.Done():
		link.Close()
		<-received
		return abort(ctx.Err())
	}
	_ = link.SetReadDeadline(time.Time{})
	resources := make(chan error, 1)
	if deadline, ok := ctx.Deadline(); ok {
		_ = copyOwner.native.conn.SetReadDeadline(deadline)
	}
	go func() { resources <- receiveCopyResourcesV1(copyOwner, req) }()
	select {
	case err = <-resources:
		if err != nil {
			return abort(err)
		}
	case <-ctx.Done():
		copyOwner.native.interrupt()
		<-resources
		return abort(ctx.Err())
	}
	_ = copyOwner.native.conn.SetReadDeadline(time.Time{})
	finishTransfer()
	return link, nil
}

// Wait returns the owned wait result. A cleanup error retains uncertain init
// ownership; it is never evidence of a clean or physically completed exit.
func (s *sandbox) Wait() wall.Exit {
	s.mu.Lock()
	started := s.cmd != nil
	s.mu.Unlock()
	if started {
		<-s.exited
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

// Destroy removes only a stopped sandbox's private runtime files.
func (s *sandbox) Destroy() error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	running := s.pidfd >= 0 || s.cmd != nil && !s.closed
	if !running {
		s.closed = true
	}
	s.mu.Unlock()
	if running {
		return setupError("stop the sandbox before destroying it", nil)
	}
	if err := s.closeUnstartedCreatorV3(); err != nil {
		return err
	}
	if s.freezer != nil {
		if err := s.freezer.closeEmpty(); err != nil {
			return setupError("retain the original guest cgroup until it is empty", err)
		}
	}
	if err := s.storage.destroy(); err != nil {
		return setupError("cannot remove sandbox files", err)
	}
	return nil
}

// Abilities reports only a live factory-created cgroup's freeze support.
func (s *sandbox) Abilities() wall.Abilities {
	s.mu.Lock()
	freezer := s.freezer
	live := !s.closed && s.cmd != nil && s.pidfd >= 0
	s.mu.Unlock()
	return wall.Abilities{Freeze: live && freezer.available()}
}

// CleanExit is trusted evidence for committing an already captured state epoch.
// It stays false until actual Wait completion, and after forced termination.
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed && s.initProven && s.cmd != nil && s.result.Err == nil && s.result.Code == 0 && !s.forced
}
