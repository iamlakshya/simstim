package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Requests to a session's holder, and what a holder says it can do.

// renameSaves is the feature of a holder whose rename saves the manifest
// under the session's lock before it answers (holder_rename.go). A holder
// of an older helper, which goes on running after the helper's upgrade,
// answers first and saves the name at its next tick, about a second
// later (renameOld).
const renameSaves = "rename.saved"

// holderFeatures are what a holder of this helper names in its hello.
var holderFeatures = []string{renameSaves}

// HolderRequest sends one request to a session's holder.
func HolderRequest(id, op string, args any, timeout time.Duration) (json.RawMessage, error) {
	return holderRequest(id, op, args, timeout, nil)
}

// holderRequest is HolderRequest. ready, when it is not nil, reads the
// holder's hello before the request goes, and can stop it with an error.
func holderRequest(id, op string, args any, timeout time.Duration, ready func(proto.Hello) error) (json.RawMessage, error) {
	l, hello, err := dialSessionHolder(id, timeout)
	if err != nil {
		return nil, err
	}
	defer l.c.Close()
	if ready != nil {
		if err := ready(hello); err != nil {
			return nil, err
		}
	}
	req := proto.Request{ID: 1, Op: op}
	if args != nil {
		req.Args, _ = json.Marshal(args)
	}
	if err := l.w.WriteJSON(proto.TRequest, req); err != nil {
		return nil, err
	}
	for {
		f, err := proto.Read(l.r)
		if err != nil {
			return nil, err
		}
		if f.Type != proto.TResponse {
			continue
		}
		var resp proto.Response
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			return nil, fmt.Errorf("read the holder's reply: %w", err)
		}
		if !resp.OK {
			if resp.Error != nil {
				return nil, resp.Error
			}
			return nil, errors.New("request failed")
		}
		return resp.Result, nil
	}
}

// holderLine is a connection to a holder, after the hellos.
type holderLine struct {
	c net.Conn
	w *proto.Writer
	r *bufio.Reader
}

// dialSessionHolder connects to the holder of session id, and trades hellos with
// it. The connection ends after timeout.
func dialSessionHolder(id string, timeout time.Duration) (*holderLine, proto.Hello, error) {
	var hello proto.Hello
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(context.Background(), "unix", paths.Socket(id))
	if err != nil {
		return nil, hello, fmt.Errorf("reach the holder of session %s: %w", id, err)
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	l := &holderLine{c: c, w: proto.NewWriter(c), r: bufio.NewReader(c)}
	if err := l.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		c.Close()
		return nil, hello, err
	}
	f, err := proto.Read(l.r)
	switch {
	case err != nil:
		c.Close()
		return nil, hello, fmt.Errorf("holder did not answer: %w", err)
	case f.Type != proto.THello:
		c.Close()
		return nil, hello, errors.New("holder did not answer")
	}
	_ = json.Unmarshal(f.Payload, &hello)
	return l, hello, nil
}

// oldHolderRuns reports whether the holder of session id is one of an
// older helper: its hello does not name renameSaves. A holder that does
// not answer its hello, or whose dial times out, counts as one so a
// guarded delete fails closed. A missing or refused socket counts as none
// only when the recorded process and unit have stopped too.
func oldHolderRuns(id string) bool {
	l, hello, err := dialSessionHolder(id, 2*time.Second)
	if err == nil {
		l.c.Close()
		return !slices.Contains(hello.Features, renameSaves)
	}
	if noHolderSocket(err) {
		return recordedHolderAlive(id)
	}
	c, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(context.Background(), "unix", paths.Socket(id))
	if err != nil {
		return !noHolderSocket(err) || recordedHolderAlive(id)
	}
	c.Close()
	return true
}

// recordedHolderAlive reports whether session id still has a live or
// starting holder process or unit when its socket is missing or refused.
// A manifest that cannot be read fails closed unless the session is gone.
func recordedHolderAlive(id string) bool {
	if holderLockHeld(id) {
		return true
	}
	m, err := LoadManifest(id)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	if m.Run.Unit != "" && unitActive(m.Run.Unit) {
		return true
	}
	return m.Run.HolderPID > 0 && (m.Run.BootID == "" || m.Run.BootID == BootID()) && holderPIDAlive(m.Run.HolderPID, id)
}

// holderPIDAlive checks the recorded process without reaping or signalling it.
// A recycled PID of another command or session is not this holder.
func holderPIDAlive(pid int, id string) bool {
	if !pidAlive(pid) {
		return false
	}
	args, err := holderProcessArgs(pid)
	return err == nil && len(args) >= 3 && args[1] == "holder" && args[2] == id
}

// noHolderSocket reports whether err says that nothing listens on a
// session's socket: the file is missing, or the kernel refused the dial.
func noHolderSocket(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

// socketPresent reports whether session id's socket exists and was not
// refused, even when a short dial times out.
func socketPresent(id string) bool {
	c, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(context.Background(), "unix", paths.Socket(id))
	if err == nil {
		c.Close()
		return true
	}
	return !noHolderSocket(err)
}
