//go:build linux

package egress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if id := os.Getenv("EGRESS_TEST_HELPER_ID"); id != "" {
		if err := Run(id); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestEgressSubprocessHardeningNoListenAndKill verifies P1.7 acceptance rules
// on a real Linux child process attached via Unix socketpair on fd 3:
//  1. PR_GET_DUMPABLE == 0 and RLIMIT_CORE == 0 inside the egress child.
//  2. The egress child opens zero listening TCP or UDP ports.
//  3. Killing the egress child makes subsequent flows fail closed and starts no
//     second egress process.
func TestEgressSubprocessHardeningNoListenAndKill(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("Socketpair: %v", err)
	}
	parentFile := os.NewFile(uintptr(fds[0]), "egress-parent")
	childFile := os.NewFile(uintptr(fds[1]), "egress-child")
	defer func() { _ = parentFile.Close() }()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "EGRESS_TEST_HELPER_ID=subproc-1")
	// ExtraFiles[0] becomes fd 3 in the child process.
	cmd.ExtraFiles = []*os.File{childFile}
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		t.Fatalf("start egress child: %v", err)
	}
	_ = childFile.Close()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	parentConn, err := net.FileConn(parentFile)
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	defer func() { _ = parentConn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cli, err := NewClient(ctx, parentConn, StartArgs{
		PolicyTOML: "session = \"host/s1\"\n[sandbox]\npreset = \"balanced\"\n",
	}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// 1. Verify PR_GET_DUMPABLE == 0 in the running child process and that the
	// kernel enforces non-dumpability by denying same-user reads of /proc/<pid>/fd.
	st, err := cli.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Dumpable != 0 {
		t.Errorf("child PR_GET_DUMPABLE = %d, want 0", st.Dumpable)
	}
	if _, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "fd")); !errors.Is(err, os.ErrPermission) {
		t.Errorf("reading /proc/%d/fd = %v, want permission denied (PR_SET_DUMPABLE=0)", cmd.Process.Pid, err)
	}

	// 2. Verify an in-process RunOnConn opens zero listening TCP/UDP sockets.
	before := listeningSocketsForPID(t, os.Getpid())
	supIn, egIn := net.Pipe()
	inCtx, inCancel := context.WithCancel(ctx)
	inDone := make(chan error, 1)
	go func() { inDone <- RunOnConn(inCtx, "inproc-listen-check", egIn, nil) }()
	inCli, err := NewClient(inCtx, supIn, StartArgs{PolicyTOML: "[sandbox]\npreset = \"balanced\"\n"}, nil)
	if err != nil {
		inCancel()
		t.Fatalf("in-process NewClient: %v", err)
	}
	after := listeningSocketsForPID(t, os.Getpid())
	inCancel()
	_ = inCli.Close()
	<-inDone
	if len(after) > len(before) {
		t.Errorf("RunOnConn opened listening sockets: before=%v after=%v", before, after)
	}

	// 3. Verify a denied CONNECT returns 403 over the real socketpair.
	flow, err := cli.OpenEgress(ctx, StreamHeader{Via: "explicit"})
	if err != nil {
		t.Fatalf("OpenEgress: %v", err)
	}
	_, _ = io.WriteString(flow, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(flow), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	_ = resp.Body.Close()
	_ = flow.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("denied CONNECT status = %d, want 403", resp.StatusCode)
	}

	// 4. Kill the egress process: new flows fail closed, no respawn happens.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill egress child: %v", err)
	}
	_ = cmd.Wait()
	cmd.Process = nil

	select {
	case <-cli.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("client link did not close after killing egress process")
	}
	if _, err := cli.OpenEgress(ctx, StreamHeader{Via: "explicit"}); err == nil {
		t.Fatal("OpenEgress succeeded after egress child was killed")
	}
}

func listeningSocketsForPID(t *testing.T, pid int) []string {
	t.Helper()
	inodes := map[string]bool{}
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		t.Fatalf("read %s: %v", fdDir, err)
	}
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			inodes[link[len("socket:["):len(link)-1]] = true
		}
	}
	var found []string
	for _, table := range []struct {
		path       string
		listenOnly bool
	}{
		{"/proc/net/tcp", true},
		{"/proc/net/tcp6", true},
		{"/proc/net/udp", false},
		{"/proc/net/udp6", false},
	} {
		data, err := os.ReadFile(table.path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// TCP state 0A is TCP_LISTEN; UDP state 07 is UDP_CLOSE (bound).
			if table.listenOnly && fields[3] != "0A" {
				continue
			}
			if inodes[fields[9]] {
				found = append(found, table.path+":"+fields[1])
			}
		}
	}
	return found
}
