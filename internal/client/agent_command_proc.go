package client

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Agent-only SSH uses real owned pipe descriptors. exec.Cmd therefore owns no
// stderr copying goroutine whose blocked writer could delay Process.Wait's
// death observation. This replaces its one copier; it adds no process or lane.
type ownedAgentStderr struct {
	budget      *agentShutdownBudget
	reader      *os.File
	done        chan error
	rawDone     chan struct{}
	finished    chan struct{}
	finishError error // published by closing finished
	rawError    error // published by closing rawDone, never changed afterwards
	exitedAt    time.Time
	waitDelay   time.Duration
}

func commandAgentProc(cmd *exec.Cmd, unreachable func(int, string) bool, observer io.Writer) (Proc, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open the command's stdin: %w", err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("open the command's stdout: %w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	owned := &ownedAgentStderr{budget: &agentShutdownBudget{}, reader: reader, done: make(chan error, 1), rawDone: make(chan struct{}), finished: make(chan struct{}), waitDelay: cmd.WaitDelay}
	p := &cmdProc{cmd: cmd, stdin: input, stdout: output, unreachable: unreachable, agentStderr: owned}
	// All three command stdio entries are files: no exec.Cmd I/O copier.
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	_ = writer.Close()
	var target io.Writer = &p.stderr
	if observer != nil {
		target = io.MultiWriter(target, observer)
	}
	go func() { _, copyErr := io.Copy(target, reader); owned.done <- copyErr }()
	return p, nil
}

func (s *ownedAgentStderr) finish() error {
	// The existing SSH wait delay is absolute from raw reaping. Expiry closes
	// ONLY our own read FD and never joins an inherited pipe/blocked writer.
	// A live copier retains its owned state/witness until it actually unwinds.
	delay := s.waitDelay
	if delay <= 0 {
		delay = sshWaitDelay
	}
	deadline := s.exitedAt.Add(delay)
	if whole := s.budget.deadline(); whole.Before(deadline) {
		deadline = whole
	}
	timer := time.NewTimer(max(time.Until(deadline), 0))
	defer timer.Stop()
	select {
	case err := <-s.done:
		closeErr := s.reader.Close()
		if err != nil {
			return agentNativeResourceError(err)
		}
		if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			return agentNativeResourceError(closeErr)
		}
		return nil
	case <-timer.C:
		_ = s.reader.Close()
		return agentNativeResourceError(errors.New("the owned SSH stderr copier has not quiesced"))
	}
}

func (p *cmdProc) observedAgentExit() (error, bool) {
	if p.agentStderr == nil {
		return nil, false
	}
	select {
	case <-p.agentStderr.rawDone:
		return p.agentStderr.rawError, true
	default:
		return nil, false
	}
}

func (s *ownedAgentStderr) failure() error {
	select {
	case <-s.finished:
		return s.finishError
	default:
		return nil
	}
}
