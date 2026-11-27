package jail

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func childrenTestHooks() {
	if os.Getenv("JAILTEST_AFTER_REAP_GATE") == "1" {
		afterMainReap = func() {
			_, _ = unix.Write(3, []byte("reaped\n"))
			_, _ = unix.Read(4, make([]byte, 1))
		}
	}
	if os.Getenv("JAILTEST_REAP_ORPHAN") == "1" {
		childReaped = func(main, child int) {
			if child != main {
				_, _ = unix.Write(3, []byte("orphan\n"))
			}
		}
	}
	if os.Getenv("JAILTEST_NO_CHILDREN") == "1" {
		restChildren = func() []int { return processChildren("/proc", os.Getpid()) }
	}
	if os.Getenv("JAILTEST_EMPTY_CHILDREN_ONCE") == "1" {
		next := restChildren
		first := true
		restChildren = func() []int {
			if first {
				first = false
				return nil
			}
			return next()
		}
	}
}

func holdInheritedFile() int {
	file := os.NewFile(3, "orphan witness")
	defer func() { _ = file.Close() }()
	if _, err := io.WriteString(file, "child-ready\n"); err != nil {
		return 2
	}
	for {
		time.Sleep(time.Hour)
	}
}

// restProbe runs init's reaping logic as an owned session leader, without
// requiring namespace or Landlock support in the test kernel.
func restProbe(how string) int {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 2
	}
	setup := os.NewFile(3, "setup")
	_, _ = io.Copy(io.Discard, setup)
	_ = setup.Close()
	script := "sleep 600 & printf 'ready\\n'"
	if how == "kill" {
		script += "; read hold"
	}
	// The direct command dies with this init. Its forked background child
	// does not inherit Pdeathsig and must be ended by the jail's group/init.
	p, err := os.StartProcess("/bin/sh", []string{"sh", "-c", script}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Pdeathsig: unix.SIGKILL},
	})
	if err != nil {
		return 3
	}
	ws := reap(p.Pid)
	_ = p.Release()
	endRest()
	status := os.NewFile(4, "status")
	_, _ = io.WriteString(status, exitStatus(ws))
	_ = status.Close()
	return ws.ExitStatus()
}

func TestOrphanPipesEndBeforeWait(t *testing.T) {
	for _, config := range []string{"normal", "no task children", "empty first listing"} {
		for _, how := range []string{"normal exit", "kill"} {
			t.Run(config+"/"+how, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				process := exec.CommandContext(context.WithoutCancel(ctx), self, restArg, how)
				process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				process.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
				switch config {
				case "no task children":
					process.Env = append(process.Env, "JAILTEST_NO_CHILDREN=1")
				case "empty first listing":
					process.Env = append(process.Env, "JAILTEST_EMPTY_CHILDREN_ONCE=1")
				}
				c := &Cmd{ctx: ctx, cmd: process, setup: []byte("{}\n"), abort: make(chan struct{}), done: make(chan struct{})}
				assertPipesBeforeWait(t, c, how == "kill")
			})
		}
	}
}

func TestLandlockOrphanPipesEndBeforeWait(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	for _, how := range []string{"normal exit", "kill"} {
		t.Run(how, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			script := "sleep 600 & printf 'ready\\n'"
			if how == "kill" {
				script += "; read hold"
			}
			c, err := Command(ctx, Spec{Argv: []string{"/bin/sh", "-c", script}, Binds: libBinds(),
				Seccomp: ProfileConverter, Env: append(probeEnv, "JAILTEST_NO_CHILDREN=1")})
			if err != nil {
				t.Fatal(err)
			}
			assertPipesBeforeWait(t, c, how == "kill")
		})
	}
}

func assertPipesBeforeWait(t *testing.T, c *Cmd, kill bool) {
	t.Helper()
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			c.killGroup() // the owned, unreaped leader still pins the group
			_ = c.Wait()
		}
	}()
	reader := bufio.NewReader(stdout)
	ready := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if line != "ready\n" && err == nil {
			err = io.ErrUnexpectedEOF
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not reach its ready marker")
	}
	if kill {
		if err := c.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
	}
	drained := make(chan error, 2)
	for _, r := range []io.Reader{reader, stderr} {
		go func() { _, err := io.ReadAll(r); drained <- err }()
	}
	for range 2 {
		select {
		case err := <-drained:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("an orphan retained a pipe before Wait was called")
		}
	}
	err = c.Wait()
	waited = true
	if !kill && err != nil {
		t.Fatalf("natural command exit = %v", err)
	}
	if kill && err == nil {
		t.Fatal("SIGKILL returned a successful command exit")
	}
}
