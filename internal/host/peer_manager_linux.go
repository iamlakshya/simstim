//go:build linux

package host

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// verifyUserManagerNamespace verifies that a systemd --user peer whose
// /proc/<pid>/ns/cgroup symlink returned EACCES (which happens on systemd >= 254
// because user@.service carries CAP_WAKE_ALARM in CapPrm while unprivileged
// user processes have empty CapEff) shares the host root user and cgroup
// namespaces with this process.
func verifyUserManagerNamespace(p peer) bool {
	if p.pid <= 0 || p.uid != os.Getuid() {
		return false
	}
	alive := p.alive
	if alive == nil {
		pidfd, err := unix.PidfdOpen(p.pid, 0)
		if err != nil {
			return false
		}
		defer func() { _ = unix.Close(pidfd) }()
		alive = func() bool { return peerPidfdAliveV1(pidfd) }
	}
	if !alive() {
		return false
	}
	proc, err := os.OpenRoot(filepath.Join(procRoot, strconv.Itoa(p.pid)))
	if err != nil {
		return false
	}
	defer func() { _ = proc.Close() }()
	cmdline, err := proc.ReadFile("cmdline")
	if err != nil {
		return false
	}
	trustedPath, err := verifyManagerCmdlineV1(cmdline)
	if err != nil {
		return false
	}
	if err := verifyTrustedSystemdBinaryV1(trustedPath); err != nil {
		return false
	}
	if err := verifyPrivilegedUserManagerProcV1(proc, p.pid, true); err != nil {
		return false
	}
	return alive()
}

func peerPidfdAliveV1(pidfd int) bool {
	if pidfd < 0 || pidfd > math.MaxInt32 {
		return false
	}
	for {
		polls := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		n, err := unix.Poll(polls, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil && n == 0
	}
}

func (manager *nativeUserManagerV1) checkManagerPeerNamespaceV1() error {
	if err := manager.peer.Check(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}
	if err := manager.peer.checkLive(); err != nil {
		return err
	}
	if err := verifyPrivilegedUserManagerProcV1(manager.peer.proc, manager.peer.pid, true); err != nil {
		return errors.Join(sessionLeaseError("the original startup connector changed cgroup namespace"), err)
	}
	return manager.peer.checkLive()
}

func (manager *nativeUserManagerV1) checkManagerExecutableV1(argv0 string) error {
	actual, err := startupExecutableInfo(manager.peer.proc)
	if err == nil {
		for _, path := range []string{"/usr/lib/systemd/systemd", "/lib/systemd/systemd"} {
			info, statErr := os.Stat(path)
			if statErr != nil {
				continue
			}
			if nativeManagerExecutableOwnerV1(actual, info) {
				return nil
			}
		}
		return sessionLeaseError("the native manager peer is not the root-owned systemd executable")
	}
	if !errors.Is(err, os.ErrPermission) {
		return err
	}
	if err := verifyPrivilegedUserManagerProcV1(manager.peer.proc, manager.peer.pid, true); err != nil {
		return errors.Join(sessionLeaseError("the native manager peer is not the root-owned systemd executable"), err)
	}
	return verifyTrustedSystemdBinaryV1(argv0)
}

func verifyTrustedSystemdBinaryV1(argv0 string) error {
	var trustedPath string
	switch argv0 {
	case "/usr/lib/systemd/systemd":
		trustedPath = "/usr/lib/systemd/systemd"
	case "/lib/systemd/systemd":
		trustedPath = "/lib/systemd/systemd"
	default:
		return sessionLeaseError("the native manager peer is not the root-owned systemd executable")
	}
	info, statErr := os.Stat(trustedPath)
	if statErr != nil || !nativeManagerExecutableOwnerV1(info, info) {
		return errors.Join(sessionLeaseError("the native manager peer is not the root-owned systemd executable"), statErr)
	}
	return nil
}

func verifyManagerCmdlineV1(raw []byte) (string, error) {
	trimmed := bytes.TrimSuffix(raw, []byte{0})
	parts := bytes.Split(trimmed, []byte{0})
	if len(parts) != 2 || string(parts[1]) != "--user" {
		return "", sessionLeaseError("the user manager command line does not match systemd --user")
	}
	argv0 := string(parts[0])
	if argv0 != "/usr/lib/systemd/systemd" && argv0 != "/lib/systemd/systemd" {
		return "", sessionLeaseError("the user manager command line does not match systemd --user")
	}
	return argv0, nil
}

func verifyPrivilegedUserManagerProcV1(proc *os.Root, pid int, requireInitScope bool) error {
	if err := verifyHostRootNamespacesV1(); err != nil {
		return err
	}
	if err := verifyManagerUserNamespaceMapsV1(proc); err != nil {
		return err
	}
	uid := os.Getuid()
	cgPeer, err := proc.ReadFile("cgroup")
	if err != nil {
		return fmt.Errorf("read user manager cgroup: %w", err)
	}
	if err := verifyManagerCgroupPathsV1(cgPeer, uid, requireInitScope); err != nil {
		return err
	}
	peerStatus, err := proc.ReadFile("status")
	if err != nil {
		return fmt.Errorf("read user manager status: %w", err)
	}
	selfStatus, err := os.ReadFile(filepath.Join(procRoot, "self", "status"))
	if err != nil {
		return fmt.Errorf("read caller status: %w", err)
	}
	return verifyManagerStatusV1(peerStatus, selfStatus, pid, uid)
}

func verifyManagerUserNamespaceMapsV1(proc *os.Root) error {
	pid1Root, err := os.OpenRoot(filepath.Join(procRoot, "1"))
	if err != nil {
		return fmt.Errorf("open pid 1 proc root: %w", err)
	}
	defer func() { _ = pid1Root.Close() }()
	selfRoot, err := os.OpenRoot(filepath.Join(procRoot, "self"))
	if err != nil {
		return fmt.Errorf("open self proc root: %w", err)
	}
	defer func() { _ = selfRoot.Close() }()
	for _, name := range []string{"uid_map", "gid_map"} {
		pid1Map, pid1Err := pid1Root.ReadFile(name)
		selfMap, selfErr := selfRoot.ReadFile(name)
		peerMap, peerErr := proc.ReadFile(name)
		if errors.Is(pid1Err, os.ErrNotExist) && errors.Is(selfErr, os.ErrNotExist) && errors.Is(peerErr, os.ErrNotExist) {
			continue
		}
		if err := errors.Join(pid1Err, selfErr, peerErr); err != nil {
			return fmt.Errorf("read user namespace %s: %w", name, err)
		}
		if err := verifyUserNamespaceMapBytesV1(pid1Map, selfMap, peerMap); err != nil {
			return err
		}
	}
	return nil
}

func verifyUserNamespaceMapBytesV1(pid1Map, selfMap, peerMap []byte) error {
	pid1Fields, err := parseUserNamespaceMapV1(pid1Map)
	if err != nil {
		return err
	}
	selfFields, err := parseUserNamespaceMapV1(selfMap)
	if err != nil {
		return err
	}
	peerFields, err := parseUserNamespaceMapV1(peerMap)
	if err != nil {
		return err
	}
	if !slices.Equal(pid1Fields, selfFields) || !slices.Equal(selfFields, peerFields) {
		return sessionLeaseError("the user manager is outside the host root user namespace")
	}
	return nil
}

func parseUserNamespaceMapV1(raw []byte) ([]uint64, error) {
	fields := strings.Fields(string(raw))
	if len(fields) < 3 || len(fields)%3 != 0 {
		return nil, sessionLeaseError("invalid user namespace ID map")
	}
	out := make([]uint64, len(fields))
	for i, field := range fields {
		v, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return nil, sessionLeaseError("invalid user namespace ID map")
		}
		if i%3 == 2 && v == 0 {
			return nil, sessionLeaseError("invalid user namespace ID map")
		}
		out[i] = v
	}
	return out, nil
}

func verifyHostRootNamespacesV1() error {
	info1, err := os.Stat(filepath.Join(procRoot, "1"))
	if err != nil {
		return fmt.Errorf("stat pid 1: %w", err)
	}
	stat1, ok := info1.Sys().(*syscall.Stat_t)
	if !ok || stat1.Uid != 0 {
		return sessionLeaseError("the caller is outside the host root user namespace")
	}
	cg1, err := os.ReadFile(filepath.Join(procRoot, "1", "cgroup"))
	if err != nil {
		return fmt.Errorf("read pid 1 cgroup: %w", err)
	}
	paths1, _ := cgroupPaths(cg1)
	if len(paths1) == 0 {
		return sessionLeaseError("pid 1 has no readable cgroup path")
	}
	for _, p := range paths1 {
		if slices.Contains(p, "..") {
			return sessionLeaseError("the caller is inside a non-root cgroup namespace")
		}
	}
	return nil
}

func verifyManagerCgroupPathsV1(cgroup []byte, uid int, requireInitScope bool) error {
	paths, _ := cgroupPaths(cgroup)
	if len(paths) == 0 || placeAmong(paths, "") != placeOutside {
		return sessionLeaseError("the user manager is inside a session or has unknown placement")
	}
	wantService := fmt.Sprintf("/user.slice/user-%d.slice/user@%d.service", uid, uid)
	wantInit := wantService + "/init.scope"
	matched := false
	for _, p := range paths {
		if slices.Contains(p, "..") {
			return sessionLeaseError("the user manager cgroup path escapes the caller's cgroup namespace")
		}
		joined := strings.Join(p, "/")
		if joined == wantInit || (!requireInitScope && joined == wantService) {
			matched = true
		}
	}
	if !matched {
		return sessionLeaseError("the native user manager is outside its original system user service")
	}
	return nil
}

func verifyManagerStatusV1(peerStatus, selfStatus []byte, pid, uid int) error {
	fields := parseProcStatusFields(peerStatus)
	pidStr := strconv.Itoa(pid)
	uidStr := strconv.Itoa(uid)
	if fields["Name"] != "systemd" || fields["Pid"] != pidStr || fields["Tgid"] != pidStr || fields["NSpid"] != pidStr || fields["PPid"] != "1" {
		return sessionLeaseError("the user manager status does not match systemd --user under pid 1")
	}
	uidFields := strings.Fields(fields["Uid"])
	if len(uidFields) != 4 || uidFields[0] != uidStr || uidFields[1] != uidStr || uidFields[2] != uidStr || uidFields[3] != uidStr {
		return sessionLeaseError("the user manager status UID does not match the caller")
	}
	gidFields := strings.Fields(fields["Gid"])
	if len(gidFields) != 4 || gidFields[0] != gidFields[1] || gidFields[0] != gidFields[2] || gidFields[0] != gidFields[3] {
		return sessionLeaseError("the user manager status GID is inconsistent")
	}
	peerCapPrm, err := strconv.ParseUint(fields["CapPrm"], 16, 64)
	if err != nil {
		return fmt.Errorf("parse user manager CapPrm: %w", err)
	}
	selfFields := parseProcStatusFields(selfStatus)
	selfCapPrm, err := strconv.ParseUint(selfFields["CapPrm"], 16, 64)
	if err != nil {
		return fmt.Errorf("parse caller CapPrm: %w", err)
	}
	if (peerCapPrm &^ selfCapPrm) == 0 {
		return sessionLeaseError("the user manager lacks elevated capabilities explaining EACCES")
	}
	return nil
}

func parseProcStatusFields(raw []byte) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return fields
}
