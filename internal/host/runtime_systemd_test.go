package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// fakeSystemd puts a fake systemctl and systemd-run on $PATH, so that the
// helper takes its systemd path. systemd-run starts the holder in the
// background with the variables of --setenv, and keeps its process ID by
// the unit's name. systemctl stop kills that process and waits for it,
// unless stops is false: then it fails. systemctl show says whether the
// unit's process still runs. It returns the folder of the process IDs.
func fakeSystemd(t *testing.T, h *hosttest.Host, stops bool) string {
	t.Helper()
	units := filepath.Join(h.Root, "units")
	if err := os.MkdirAll(units, 0o700); err != nil {
		t.Fatal(err)
	}
	h.Script("systemd-run", `units=`+units+`
while [ $# -gt 0 ]; do
	case "$1" in
	--unit=*) unit=${1#--unit=} ;;
	--setenv=*) export "${1#--setenv=}" ;;
	-p) shift ;;
	-*) ;;
	*) break ;;
	esac
	shift
done
"$@" </dev/null >/dev/null 2>&1 &
echo $! > "$units/$unit"
`)
	stop := `pid=$(cat "$units/$3"); kill -TERM "$pid"; while kill -0 "$pid" 2>/dev/null; do sleep 0.05; done`
	if !stops {
		stop = `echo "Failed to stop $3: the test says no" >&2; exit 1`
	}
	h.Script("systemctl", `units=`+units+`
case "$2" in
is-system-running) echo running ;;
stop) if [ ! -f "$units/$3" ]; then exit 0; fi
	`+stop+` ;;
show) if kill -0 "$(cat "$units/$3" 2>/dev/null)" 2>/dev/null; then echo active; else echo inactive; fi ;;
*) exit 0 ;;
esac
`)
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	return units
}

// unitPID is the process ID that the fake systemd-run kept for the unit of
// session id.
func unitPID(t *testing.T, units, id string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(units, host.UnitName(id)))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// On the systemd path, a start whose secrets cannot reach its holder stops
// the holder's unit, and deletes the session once the unit is inactive.
// When the stop fails, it keeps the session, and its error says why.
func TestAFailedDeliveryStopsTheUnit(t *testing.T) {
	for _, stops := range []bool{true, false} {
		h := hosttest.New(t)
		units := fakeSystemd(t, h, stops)
		old := *host.Deliver
		var id string
		*host.Deliver = func(sid string, _ map[string]api.Secret) error {
			id = sid
			return errors.New("the test drops the secrets")
		}
		_, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24,
			Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
		*host.Deliver = old
		if id == "" {
			t.Fatalf("stops %v: the start did not reach the delivery: %v", stops, err)
		}
		pid := unitPID(t, units, id)
		var pe *proto.Error
		switch {
		case stops:
			if codeOf(err) != "secrets_failed" || holderRuns(t, id) || len(sessionFolders(t)) != 0 {
				t.Errorf("a unit that stops: %v, holder runs %v, folders %v", err, holderRuns(t, id), sessionFolders(t))
			}
		default:
			if !errors.As(err, &pe) || pe.Code != "secrets_failed" || !strings.Contains(pe.Message, "could not be stopped, so the session "+id+" was kept") ||
				len(sessionFolders(t)) != 1 {
				t.Errorf("a unit that does not stop: %v, folders %v", err, sessionFolders(t))
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
			if err := hosttest.Reap(pid, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(units, host.UnitName(id))); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// On the systemd path, the holder records the unit that launchHolder names
// to it, in its first save and in its last one, also where its cgroup
// does not show the unit, as here and with cgroup v1.
func TestTheHolderRecordsTheUnitThatSystemdRunNames(t *testing.T) {
	h := hosttest.New(t)
	units := fakeSystemd(t, h, true)
	s := start(t, "a", "sleep", "60")
	pid := unitPID(t, units, s.ID)
	if m := manifest(t, s.ID); m.Run.Unit != host.UnitName(s.ID) || m.Run.AgentPID == 0 {
		t.Errorf("after the start: run %+v", m.Run)
	}
	_, _ = host.HolderRequest(s.ID, "kill", nil, 3*time.Second)
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// Reap proved the owned fixture child has exited. Retire its manager
	// record before cleanup, so the fixture never signals a recycled PID.
	if err := os.Remove(filepath.Join(units, host.UnitName(s.ID))); err != nil {
		t.Fatal(err)
	}
	if m := manifest(t, s.ID); m.Run.Unit != host.UnitName(s.ID) {
		t.Errorf("after the holder's last save: run %+v", m.Run)
	}
}
