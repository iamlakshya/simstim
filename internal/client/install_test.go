package client_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// noHelper is a host without this client's helper: the bridge does not
// start, and every other command is only recorded.
type noHelper struct {
	mu   sync.Mutex
	runs []string
}

func (n *noHelper) Start(context.Context, string) (client.Proc, error) { return deadProc{}, nil }

func (n *noHelper) Run(_ context.Context, remote string, _ io.Reader) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.runs = append(n.runs, remote)
	return nil, errors.New("no answer")
}

func (n *noHelper) ReachHint() string { return "check the fake" }

func (n *noHelper) ran() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.runs...)
}

// deadProc is a bridge whose helper is not there.
type deadProc struct{}

func (deadProc) Stdin() io.WriteCloser { return nopCloser{io.Discard} }
func (deadProc) Stdout() io.Reader     { return strings.NewReader("") }
func (deadProc) Wait() error           { return errors.New("exit status 127") }
func (deadProc) Kill()                 {}
func (deadProc) Stderr() string        { return "sh: 1: helper: No such file or directory" }

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// A dial in a context from WithoutInstall never probes, installs or
// prunes: a missing helper is an error that names host upgrade. Without
// it, the dial goes on to probe the host for the install.
func TestDialWithoutInstall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tr := &noHelper{}
	h := register(t, clienttest.NewMachine(client.MachineRunning, tr))
	if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
		t.Fatal(err)
	}
	_, err := client.Dial(client.WithoutInstall(context.Background()), h, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "helper_missing" || ue.Hint != brand.Name+" host upgrade box" ||
		!strings.HasPrefix(ue.Msg, "the helper ") || !strings.HasSuffix(ue.Msg, " is not on box") {
		t.Fatalf("err = %+v", err)
	}
	if runs := tr.ran(); len(runs) != 0 {
		t.Fatalf("a dial without install ran %q", runs)
	}
	if _, err := client.Dial(context.Background(), h, false); err == nil || len(tr.ran()) == 0 {
		t.Fatalf("a dial that may install did not probe the host: %v, %q", err, tr.ran())
	}
}
