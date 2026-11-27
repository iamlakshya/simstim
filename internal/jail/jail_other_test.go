//go:build !linux

package jail

import (
	"os"
	"testing"
)

// TestNoJailsOutsideLinux checks that every jail is refused here, with a
// hint, and that Userns is false.
func TestNoJailsOutsideLinux(t *testing.T) {
	Init()
	if Userns() {
		t.Error("Userns is true outside Linux")
	}
	s, _ := goodSpec(t)
	if cmd, err := Command(t.Context(), s); cmd != nil || err == nil || hintOf(err) == "" {
		t.Errorf("Command = %v, %v; want a refusal with a hint", cmd, err)
	}
	if _, err := Command(t.Context(), Spec{}); err == nil || hintOf(err) == "" {
		t.Errorf("Command with no command = %v, want a refusal with a hint", err)
	}
	var c commander = &Cmd{}
	_, e1 := c.CombinedOutput()
	_, e2 := c.StdinPipe()
	_, e3 := c.StdoutPipe()
	_, e4 := c.StderrPipe()
	for i, err := range []error{c.Start(), c.Wait(), c.Run(), e1, e2, e3, e4, c.Signal(os.Interrupt)} {
		if err == nil || hintOf(err) == "" {
			t.Errorf("method %d = %v, want the error that jails need Linux", i, err)
		}
	}
	if c.Pid() != 0 || c.ExitCode() != -1 {
		t.Errorf("Pid %d and ExitCode %d, want 0 and -1", c.Pid(), c.ExitCode())
	}
	dir := t.TempDir() + "/gone"
	if err := os.MkdirAll(dir+"/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(t.Context(), dir); err != nil {
		t.Errorf("RemoveAll = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("RemoveAll left %s: %v", dir, err)
	}
}
