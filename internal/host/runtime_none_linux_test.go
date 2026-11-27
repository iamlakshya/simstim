//go:build linux

package host_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// These process numbers are diagnostics for namespace comparison only. They
// never issue a signal, source, native ownership or permission to mutate.
func noneDiagnosticParentV1(t *testing.T, pid int) int {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	t.Fatal("the owned runtime has no diagnostic parent")
	return 0
}

func TestNewNoneUsesSupervisorAndSharesEveryNamespace(t *testing.T) {
	hosttest.New(t)
	s := start(t, "unwalled", "/bin/sh", "-c", "printf NONE-READY; exec sleep 60")
	if s.Wall == nil || s.Wall.Level != "none" || s.Wall.Driver != "none" {
		t.Fatalf("new none did not enter the supervisor path: %+v", s.Wall)
	}
	waitOutput(t, s.ID, "NONE-READY")
	m := live(t, s.ID)
	if !host.IsNoneSupervisor(m) || m.Life == nil || m.Run.HolderPID <= 0 || m.Run.AgentPID != 0 || m.Guest == nil || m.Guest.AgentPID <= 0 {
		t.Fatal("none has no original supervisor and guest claim boundary")
	}
	holder := noneDiagnosticParentV1(t, m.Guest.AgentPID)
	init := noneDiagnosticParentV1(t, holder)
	if parent := noneDiagnosticParentV1(t, init); parent != m.Run.HolderPID {
		t.Fatal("the actual guest init is not the original supervisor's child")
	}
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/ns", m.Run.HolderPID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		want, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/ns", m.Run.HolderPID), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/ns", init), entry.Name()))
		if err != nil || got != want {
			t.Fatalf("none changed namespace %s: %q want %q (%v)", entry.Name(), got, want, err)
		}
	}
	children, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", m.Run.HolderPID, m.Run.HolderPID))
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range strings.Fields(string(children)) {
		command, err := os.ReadFile(filepath.Join("/proc", child, "cmdline"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(command, []byte("\x00egress\x00")) {
			t.Fatal("an unwalled supervisor started egress")
		}
	}
}

func TestNoneAttachPreservesLiveBytes(t *testing.T) {
	hosttest.New(t)
	s := start(t, "none-bytes", "/bin/sh", "-c", "stty raw -echo; printf 'READY\\n'; head -c 1 >/dev/null; printf 'LIVE\\033[?2004h\\000\\377\\n'; exec sleep 60")
	waitOutput(t, s.ID, "READY")
	b := newRawBridge(t)
	b.attach(s.ID, -1, 80, 24)
	if got := b.output(0, "READY"); !bytes.Equal(got, []byte("READY\n")) {
		t.Fatalf("none replay changed: %q", got)
	}
	b.send(proto.TInput, []byte("x"))
	want := []byte("LIVE\x1b[?2004h\x00\xff\n")
	if got := b.output(6, "LIVE"); !bytes.Equal(got, want) {
		t.Fatalf("none live bytes changed: %q want %q", got, want)
	}
}
