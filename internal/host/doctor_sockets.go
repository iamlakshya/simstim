package host

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// socketsName is the doctor's name for the line about the session sockets.
const socketsName = "session sockets"

// socketsState is a holder's answer to the op "sockets".
type socketsState struct {
	PeerCheck bool   `json:"peer_check"`
	Version   string `json:"version"`
}

// socketsCheck is the doctor's line about the session sockets (spec 10.4).
// It is weak without a systemd user manager, and weak while any holder runs
// without the peer check: weak names those sessions.
func socketsCheck(systemd bool, weak []string) Check {
	if !systemd {
		return Check{Name: socketsName,
			Detail: "weak: without a unit for each session, any of your programs can attach to, type into or stop a session",
			Fix:    "run sessions under systemd's user manager: use a host with systemd, where systemctl --user status works"}
	}
	if len(weak) > 0 {
		runs := plural(len(weak), "session still runs", "sessions still run")
		return Check{Name: socketsName,
			Detail: fmt.Sprintf("weak: %s without the peer check, so its socket lets any program in: %s", runs, strings.Join(weak, ", ")),
			Fix:    "resume them: quit each one's agent, then run " + brand.Name + " resume <host>/<session>"}
	}
	return Check{Name: socketsName, OK: true,
		Detail: "a program in a session gets only a ping on a session's socket, and the helper refuses to act on a session for it. " +
			"A program that leaves the sessions' slice, with systemd-run --user or a cgroup.procs file, still can"}
}

// weakHolders asks each running holder whether it checks its peers, and
// names the sessions whose holder does not. That is a holder from an older
// helper, which does not know the op, whatever files lie beside it. It is
// also a holder that started without a systemd user manager, and still
// runs without the check after the manager came back.
func weakHolders() []string {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return nil
	}
	var weak []string
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !validID(id) || !alive(id) {
			continue
		}
		why := ""
		var st socketsState
		res, err := HolderRequest(id, "sockets", nil, 2*time.Second)
		switch {
		case err != nil || json.Unmarshal(res, &st) != nil:
			why = "an older helper"
		case !st.PeerCheck:
			why = "started without systemd's user manager"
		default:
			continue
		}
		name := id
		if m, err := LoadManifest(id); err == nil && m.Name != "" {
			name = m.Name
		}
		weak = append(weak, fmt.Sprintf("%s (%s, %s)", name, id, why))
	}
	sort.Strings(weak)
	return weak
}
