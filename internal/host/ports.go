package host

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// Port is a TCP port a session's processes listen on.
type Port = api.Port

// sessionPIDs returns the processes of this holder's session: its own systemd
// unit's cgroup when it has one, or else the agent and its descendants.
func sessionPIDs(unit bool, agentPID int) []int {
	if !unit {
		return descendants(agentPID)
	}
	var pids []int
	b, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "0::"); ok {
				procs, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", rest, "cgroup.procs")) //nolint:gosec // G703: rest is the kernel's own cgroup path, from /proc/self/cgroup
				if err == nil {
					for _, f := range strings.Fields(string(procs)) {
						if n, err := strconv.Atoi(f); err == nil && n != os.Getpid() {
							pids = append(pids, n)
						}
					}
				}
			}
		}
	}
	return pids
}

// GuestListeningPorts returns only descendant listener claims in this network namespace.
func GuestListeningPorts() []Port { return listeningPorts(descendants(os.Getpid())) }

// listeningPorts finds TCP listening sockets that belong to the given processes.
func listeningPorts(pids []int) []Port {
	owner := map[string]int{} // socket inode -> pid
	for _, pid := range pids {
		fds, err := os.ReadDir(fmt.Sprintf("%s/%d/fd", procRoot, pid))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(fmt.Sprintf("%s/%d/fd/%s", procRoot, pid, fd.Name()))
			if err != nil {
				continue
			}
			if inode, ok := strings.CutPrefix(link, "socket:["); ok {
				owner[strings.TrimSuffix(inode, "]")] = pid
			}
		}
	}
	seen := map[int]bool{}
	var out []Port
	for _, file := range []string{procRoot + "/net/tcp", procRoot + "/net/tcp6"} {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || fl[3] != "0A" { // 0A = LISTEN
				continue
			}
			pid, ok := owner[fl[9]]
			if !ok {
				continue
			}
			addr, port := parseHexAddr(fl[1])
			if port == 0 || seen[port] {
				continue
			}
			seen[port] = true
			comm, _ := os.ReadFile(fmt.Sprintf("%s/%d/comm", procRoot, pid))
			out = append(out, Port{Port: port, Address: addr, PID: pid, Process: strings.TrimSpace(string(comm))})
		}
		f.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// parseHexAddr parses /proc/net/tcp's "0100007F:0BB8" (little-endian words).
func parseHexAddr(s string) (string, int) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return "", 0
	}
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", int(port)
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw).String(), int(port)
}

// descendants returns pid and all its descendants.
func descendants(pid int) []int {
	children := map[int][]int{}
	entries, _ := os.ReadDir(procRoot)
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procRoot, n))
		if err != nil {
			continue
		}
		// The parent PID is the second field after the command, which is in parentheses.
		if i := strings.LastIndexByte(string(b), ')'); i > 0 {
			f := strings.Fields(string(b)[i+1:])
			if len(f) > 1 {
				if pp, err := strconv.Atoi(f[1]); err == nil {
					children[pp] = append(children[pp], n)
				}
			}
		}
	}
	out := []int{pid}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}
