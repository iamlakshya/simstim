package e2b

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/testhome"
)

// The fake envd runs each command under a wrapper: this test binary, started
// again with wrapperEnv set. The wrapper leads a process group of its own and
// runs the command in it. It reports how the command ended, and stays alive
// until the fake lets it go. Then it kills its whole group. So the fake never
// signals a process or a group by its number, and the children that a command
// leaves behind end when the fake shuts down.
const (
	wrapperEnv       = "E2B_TEST_FAKE_ENVD_WRAPPER"
	failLeadGroupEnv = "E2B_TEST_FAKE_ENVD_FAIL_PGID"
)

func TestMain(m *testing.M) {
	if os.Getenv(wrapperEnv) != "" {
		// The fake sets atexit_sleep_ms=0 in the wrapper's GORACE, so the
		// race detector reports a race, with exit status 66, but does not
		// sleep a second at exit.
		os.Exit(runWrapper(os.Args[1:]))
	}
	testhome.Main(m)
}

// runWrapper is the wrapper's main. Its stdin, stdout and stderr go to the
// command. File 3 carries its report to the fake, file 4 the fake's orders,
// and file 5 is the test's own stderr. The order "kill" kills the command
// alone, as envd's SIGKILL does, and the end of the orders ends the wrapper
// and its group.
func runWrapper(args []string) int {
	report := os.NewFile(3, "report")
	orders := os.NewFile(4, "orders")
	group, err := leadGroup()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "lead process group: %v\n", err)
		_, _ = fmt.Fprintf(report, "error %v\n", err)
		_ = report.Close()
		return 1
	}
	// Files 3, 4 and 5 are the wrapper's own: the command does not get them.
	keepFromCommand(3, 4, 5)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = withoutEnv(os.Environ(), wrapperEnv, failLeadGroupEnv)
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(report, "error %v\n", err)
		return 1
	}
	// Only the command holds its streams now, so they end when it does. The
	// wrapper's own stderr becomes the test's, so a race or a panic in the
	// wrapper shows in the test's output.
	_ = os.Stdin.Close()
	_ = os.Stdout.Close()
	useTestStderr(5)
	go func() {
		_, _ = fmt.Fprintln(report, endStatus(cmd.Wait()))
		_ = report.Close()
	}()
	sc := bufio.NewScanner(orders)
	for sc.Scan() {
		if sc.Text() == "kill" {
			_ = cmd.Process.Kill()
		}
	}
	endGroup(cmd, group)
	return 0
}

func withoutEnv(env []string, names ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		drop := false
		for _, name := range names {
			if strings.HasPrefix(kv, name+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// endStatus says how a command ended, in one line: "exit <code>" or
// "signal <name>".
func endStatus(err error) string {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return "exit 0"
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "signal " + ws.Signal().String()
		}
		return fmt.Sprintf("exit %d", ee.ExitCode())
	default:
		return "error " + err.Error()
	}
}

// fakeCmd is one command that the fake runs under a wrapper.
type fakeCmd struct {
	wrapper *exec.Cmd
	stdin   *os.File // the write end of the command's stdin
	stdout  *fakeOut // the read end of its stdout
	stderr  *fakeOut // the read end of its stderr
	// ended closes when the wrapper has said how the command ended. Then
	// status is its line, or "" when the wrapper went first.
	ended  chan struct{}
	status string

	mu     sync.Mutex
	orders *os.File // the write end of the wrapper's orders; nil once closed
}

// startWrapped starts script with /bin/sh under a wrapper, with home as HOME
// when it is set.
func startWrapped(script, home string, failGroup bool) (*fakeCmd, error) {
	var ends [10]*os.File // read and write ends of five pipes
	for i := 0; i < len(ends); i += 2 {
		r, w, err := os.Pipe()
		if err != nil {
			closeAll(ends[:i]...)
			return nil, fmt.Errorf("make a pipe: %w", err)
		}
		ends[i], ends[i+1] = r, w
	}
	inR, inW, outR, outW, errR, errW, repR, repW, ordR, ordW := ends[0], ends[1], ends[2], ends[3], ends[4], ends[5], ends[6], ends[7], ends[8], ends[9]
	w := exec.Command(os.Args[0], "/bin/sh", "-c", script)
	gorace := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
	w.Env = append(os.Environ(), wrapperEnv+"=1", "GORACE="+gorace)
	if failGroup {
		w.Env = append(w.Env, failLeadGroupEnv+"=1")
	}
	if home != "" {
		w.Env = append(w.Env, "HOME="+home)
	}
	w.Stdin, w.Stdout, w.Stderr = inR, outW, errW
	w.ExtraFiles = []*os.File{repW, ordR, os.Stderr}
	err := w.Start()
	// The wrapper holds its own copies of these ends now.
	closeAll(inR, outW, errW, repW, ordR)
	if err != nil {
		closeAll(inW, outR, errR, repR, ordW)
		return nil, fmt.Errorf("start the wrapper: %w", err)
	}
	c := &fakeCmd{wrapper: w, stdin: inW, stdout: newFakeOut(outR), stderr: newFakeOut(errR), ended: make(chan struct{}), orders: ordW}
	go func() {
		line, _ := bufio.NewReader(repR).ReadString('\n')
		_ = repR.Close()
		c.status = strings.TrimSpace(line)
		close(c.ended)
	}()
	return c, nil
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

func (c *fakeCmd) cutOutput() {
	c.stdout.cut()
	c.stderr.cut()
}

func (c *fakeCmd) closeOutput() {
	_ = c.stdout.Close()
	_ = c.stderr.Close()
}

// pid is the wrapper's process ID. The fake reaps the wrapper only after it
// lets it go, so the ID stays the wrapper's until then.
func (c *fakeCmd) pid() uint32 { return uint32(c.wrapper.Process.Pid) }

// kill kills the command, and not its children, as envd's SIGKILL does.
func (c *fakeCmd) kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.orders != nil {
		_, _ = c.orders.WriteString("kill\n")
	}
}

// release lets the wrapper go: it kills its group, the command and every
// child that the command left behind, and exits.
func (c *fakeCmd) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.orders != nil {
		_ = c.orders.Close()
		c.orders = nil
	}
}

// endEvent turns the wrapper's report into envd's end event.
func endEvent(report string) map[string]any {
	kind, arg, _ := strings.Cut(report, " ")
	switch kind {
	case "exit":
		code, _ := strconv.Atoi(arg)
		if code == 0 {
			return map[string]any{"exited": true}
		}
		return map[string]any{"exited": true, "exitCode": code}
	case "signal":
		return map[string]any{"exitCode": -1, "exited": false, "status": "signal: " + arg}
	default: // the wrapper went first, or the command did not start
		return map[string]any{"exitCode": -1, "exited": false, "status": "signal: killed"}
	}
}
