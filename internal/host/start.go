package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// startArgv wraps an agent's argv in a short sh script when something must
// show in the session's terminal before the agent: notes, then a hook, which
// gets vars in its environment and whose output is part of the session. A
// failing hook is reported with its exit code, and the agent starts anyway.
// The script then execs the agent, so the agent is the session's process.
func startArgv(argv []string, hook string, vars, notes []string) []string {
	if hook == "" && len(notes) == 0 {
		return argv
	}
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "printf '%%s\\n\\n' %s; ", shellQuote(n))
	}
	label := brand.Name
	if hook != "" {
		name := shellQuote(brand.Name)
		fmt.Fprintf(&b, `printf '[%%s: running %%s]\n' %s "$0"; `, name)
		if len(vars) > 0 {
			b.WriteString(quoteVars(vars) + " ")
		}
		fmt.Fprintf(&b, `"$0"; s=$?; [ $s -eq 0 ] || printf '\n[%%s: %%s exited with %%s; the agent starts anyway]\n\n' %s "$0" $s; `, name)
		label = hook
	}
	b.WriteString(`exec "$@"`)
	return append([]string{"/bin/sh", "-c", b.String(), label}, argv...)
}

// deliver hands a new session's secrets to its holder. Tests make it fail.
var deliver = deliverSecrets

// stopStarting stops the holder of m, a start that failed, before the
// start deletes the session, and reports an error when it cannot establish
// that the holder stopped. The holder still waits for its secrets, so it
// answers no request to stop: its unit stops, or proc, the process that
// launchHolder started. A holder that went on would start its agent later,
// and save into the deleted folder, so the session is kept then.
func stopStarting(m *Manifest, proc *os.Process) error {
	switch {
	case m.Run.Unit != "":
		return stopUnitChecked(m.Run.Unit)
	case proc != nil:
		return stopProcess(proc)
	}
	return errors.New("the start knows neither the holder's unit nor its process")
}

// stopUnitChecked stops unit, and returns once systemd says that it is no
// longer active, whatever the stop answered: a unit that systemd already
// removed, as after its holder crashed, is stopped too. A question that
// fails, or a unit still active after 3 seconds, is an error, with the
// stop's error when it failed.
func stopUnitChecked(unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, stopErr := systemctl(ctx, "stop", unit)
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := systemctl(ctx, "show", unit, "-p", "ActiveState", "--value")
		if err != nil {
			return errors.Join(stopErr, err)
		}
		switch state := strings.TrimSpace(string(out)); {
		case state == "inactive" || state == "failed":
			return nil
		case time.Now().After(deadline):
			return errors.Join(stopErr, fmt.Errorf("the unit %s is still %s", unit, state))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// keptError is the error of a start that kept its session, because it
// could not establish that the session's holder stopped. The session's
// folders stay with it.
type keptError struct{ err *proto.Error }

func (e *keptError) Error() string { return e.err.Error() }

func (e *keptError) Unwrap() error { return e.err }

// holderEnded reports whether the holder of a start that failed recorded
// its own end in the session's manifest, as a holder whose agent could not
// start does before it exits: the session then stays, with that record.
// Only an end without an agent counts, as waitStarted reads one: a holder
// that recorded nothing yet may still be starting, late, and one that
// recorded its agent working is late too. So is one that recorded an end
// after its agent started: the agent may still run.
func holderEnded(id string) bool {
	m, err := LoadManifest(id)
	return err == nil && (m.Status.State == StateErrored || m.Status.State == StateExited) && m.Run.AgentPID == 0
}

// lateStart runs when a start gave up waiting for its holder, before it
// looks at what the holder recorded. Tests let the holder start its agent
// there.
var lateStart = func(string) {}

// removeFoldersAfter removes missing, the folders that a start of the
// session id made, when the start failed with err and the session is
// gone. A session that a failed start kept, as one whose agent could not
// start, keeps its folder: a resume starts the agent there.
func removeFoldersAfter(err error, id string, missing []string) {
	if err == nil {
		return
	}
	if _, serr := os.Stat(paths.SessionDir(id)); errors.Is(serr, os.ErrNotExist) {
		removeFolders(missing)
	}
}

// abandonStart ends a start of m that failed after it launched m's holder:
// proc without systemd, or m's unit. It stops the holder, and deletes the
// session once the holder is known to have stopped. Otherwise it keeps the
// session, and returns a *keptError that says why, from code, msg and the
// stop's error: the holder may still start the agent later, without the
// secrets that the start did not give it. The caller holds the lock.
func abandonStart(m *Manifest, proc *os.Process, code, msg string) (kept *keptError) {
	if serr := stopStarting(m, proc); serr != nil {
		return &keptError{err: UserError(code, msg+", and its holder could not be stopped, so the session "+m.ID+
			" was kept, and its agent may start later without its secrets: "+serr.Error(),
			"Check the host with the host doctor, then delete the session "+m.ID+".")}
	}
	_ = deleteLocked(m.ID, nil, &DeleteResult{})
	return nil
}

// signalHolder sends sig to the holder's process. Tests make it do nothing,
// or drop SIGTERM alone.
var signalHolder = func(p *os.Process, sig os.Signal) error { return p.Signal(sig) }

// stopSignals are the signals that stopProcess sends, in turn.
var stopSignals = []os.Signal{syscall.SIGTERM, syscall.SIGKILL}

// stopProcess stops p, a holder that this process started, with SIGTERM,
// then SIGKILL after stopWait, and waits for it. Wait reaps that very
// process, so a process ID that another process took since never passes
// for it. A process that is still there after the SIGKILL's wait is an
// error.
func stopProcess(p *os.Process) error {
	done := make(chan error, 1)
	go func() {
		_, err := p.Wait()
		done <- err
	}()
	for _, sig := range stopSignals {
		_ = signalHolder(p, sig)
		select {
		case err := <-done:
			return err
		case <-time.After(stopWait):
		}
	}
	return fmt.Errorf("the holder, process %d, did not stop", p.Pid)
}

// waitStarted waits until the holder has recorded its agent's start (or
// failure) in the manifest, and returns that manifest. The holder writes it
// after its socket answers, and later still when it first waits for secrets,
// so reading it at once would see, and could save over, the record of a
// session that has not started.
func waitStarted(id string, timeout time.Duration) *Manifest {
	deadline := time.Now().Add(timeout)
	for {
		m, err := LoadManifest(id)
		if err == nil && (m.Run.AgentPID != 0 || m.Status.State != StateStarting) {
			return m
		}
		if time.Now().After(deadline) {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
}
