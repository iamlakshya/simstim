//go:build linux

package chv_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

// TestCloudHypervisorLiveKVM runs a real Cloud Hypervisor v55.0 microvm proof
// on a Linux host with /dev/kvm (such as host-a). It is skipped unless
// CHV_E2E=1 is set along with:
//   - CHV_E2E_RUNTIME_DIR: dir containing pinned cloud-hypervisor, ch-remote, vmlinux
//   - CHV_E2E_TOOLS_IMG, CHV_E2E_TOOLS_HASH, CHV_E2E_TOOLS_BLOCKS
//   - CHV_E2E_ROOTFS_IMG, CHV_E2E_ROOTFS_HASH, CHV_E2E_ROOTFS_BLOCKS
//   - CHV_E2E_AGENTS_IMG
func TestCloudHypervisorLiveKVM(t *testing.T) {
	if os.Getenv("CHV_E2E") != "1" {
		t.Skip("set CHV_E2E=1 on a KVM host to run live Cloud Hypervisor microvm tests")
	}

	rp := chv.RuntimePathsIn(mustEnv(t, "CHV_E2E_RUNTIME_DIR"))
	toolsDisk := chv.DiskVerity{
		Path:          mustEnv(t, "CHV_E2E_TOOLS_IMG"),
		RootHash:      mustEnv(t, "CHV_E2E_TOOLS_HASH"),
		DataBlocks:    mustEnvUint64(t, "CHV_E2E_TOOLS_BLOCKS"),
		HasSuperblock: false,
	}
	rootfsDisk := &chv.DiskVerity{
		Path:          mustEnv(t, "CHV_E2E_ROOTFS_IMG"),
		RootHash:      mustEnv(t, "CHV_E2E_ROOTFS_HASH"),
		DataBlocks:    mustEnvUint64(t, "CHV_E2E_ROOTFS_BLOCKS"),
		HasSuperblock: true,
	}
	agentsDisk := mustEnv(t, "CHV_E2E_AGENTS_IMG")
	bwrapPath := os.Getenv("CHV_E2E_BWRAP")

	ctx := context.Background()

	t.Run("self-test serial boot", func(t *testing.T) {
		dir := t.TempDir()
		sb := chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:       rp.VMM,
				CHRemotePath:  rp.CHRemote,
				APISocket:     filepath.Join(dir, "self.vmm"),
				VsockSocket:   filepath.Join(dir, "self.vsock"),
				KernelPath:    rp.Kernel,
				CPUs:          1,
				MemoryBytes:   512 << 20,
				Tools:         toolsDisk,
				SerialConsole: true,
				InitArgs:      []string{"wall-init", "--self-test"},
			},
			BwrapPath: bwrapPath,
			SkipVsock: true,
		})
		if _, err := sb.Start(ctx); err != nil {
			t.Fatalf("Start self-test: %v", err)
		}
		defer func() { _ = sb.Destroy() }()

		done := make(chan struct{})
		go func() {
			sb.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = sb.Kill()
			t.Fatalf("self-test timed out; output:\n%s", sb.Stderr())
		}
		if !strings.Contains(sb.Stderr(), "self-test ok") {
			t.Fatalf("missing self-test ok in serial output:\n%s", sb.Stderr())
		}
		t.Logf("self-test ok; exit=%+v", sb.Wait())
	})

	t.Run("full microvm boot uid separation reset home state dirs and reboot", func(t *testing.T) {
		dir := t.TempDir()
		sessionDisk := filepath.Join(dir, "session.img")

		bootAndRun := func(initArgs []string, cmdLine string, egSrv *egress.Server) guestRunResult {
			sb := chv.NewSandbox(chv.SandboxOptions{
				Config: chv.VMConfig{
					VMMPath:         rp.VMM,
					CHRemotePath:    rp.CHRemote,
					APISocket:       filepath.Join(dir, "vm.vmm"),
					VsockSocket:     filepath.Join(dir, "vm.vsock"),
					KernelPath:      rp.Kernel,
					CPUs:            2,
					MemoryBytes:     512 << 20,
					Tools:           toolsDisk,
					Image:           rootfsDisk,
					AgentsDiskPath:  agentsDisk,
					SessionDiskPath: sessionDisk,
					SessionDirect:   true,
					InitArgs:        initArgs,
				},
				SessionDiskBytes: 64 << 20,
				BwrapPath:        bwrapPath,
				BootTimeout:      45 * time.Second,
			})
			conn, err := sb.Start(ctx)
			if err != nil {
				t.Fatalf("Start microvm: %v\nstderr:\n%s", err, sb.Stderr())
			}
			defer func() {
				_ = conn.Close()
				_ = sb.Kill()
				_ = sb.Destroy()
			}()

			info, err := chv.Info(ctx, rp.CHRemote, filepath.Join(dir, "vm.vmm"))
			if err != nil {
				t.Fatalf("ch-remote info: %v", err)
			}
			t.Logf("ch-remote info state=%q vsock_reply=%q", info.State, sb.VsockReply())

			res := runGuestCommandOverLink(ctx, t, conn, cmdLine, egSrv)
			res.vsockReply = sb.VsockReply()

			// Shut down cleanly using shipped ch-remote power-button.
			if err := sb.Stop(ctx, 15*time.Second); err != nil {
				t.Fatalf("Stop via ch-remote power-button: %v", err)
			}
			ex := sb.Wait()
			if ex.Code != 0 || ex.Err != nil {
				t.Fatalf("expected clean exit 0 after power-button, got %+v\nstderr:\n%s", ex, sb.Stderr())
			}
			if err := sb.Destroy(); err != nil {
				t.Fatalf("Destroy: %v", err)
			}
			for _, sock := range []string{filepath.Join(dir, "vm.vmm"), filepath.Join(dir, "vm.vsock")} {
				if _, err := os.Stat(sock); !os.IsNotExist(err) {
					t.Fatalf("socket left behind after Destroy: %s", sock)
				}
			}
			return res
		}

		// Boot 1: verify kernel, UID 1000 agent vs UID 900 holder, ptrace EPERM,
		// network interfaces (only lo + dummy0/dummy0p), /etc/resolv.conf, no /dev/fuse,
		// write ~/.bashrc (must disappear), /work/persist.txt & ~/.claude/projects/kept.txt (must stay).
		checkScript := strings.Join([]string{
			`echo "KERNEL=$(uname -r)"`,
			`echo "DISTRO=$(grep '^ID=' /etc/os-release | cut -d= -f2)"`,
			`echo "ID=$(id -u):$(id -g):$(id -un)"`,
			`SERIALS=""; for d in vda vdb vdc vdd; do s=$(tr -d '\0\n ' </sys/block/$d/serial 2>/dev/null || echo missing); SERIALS="$SERIALS $d=$s"; done; echo "SERIALS=$SERIALS"`,
			`echo "NET=$(ls /sys/class/net | sort | tr '\n' ',')"`,
			`echo "RESOLV=$(cat /etc/resolv.conf | tr '\n' ' ')"`,
			`case "$PATH" in /opt/agents/bin:*) echo "AGENT_PATH_FIRST=OK";; *) echo "AGENT_PATH_FIRST=FAIL:$PATH";; esac`,
			`echo "AGENT_RUN=$(probe-agent 2>&1)"`,
			`(touch /opt/agents/ro-test 2>&1 && echo "AGENTS_RO=FAIL") || echo "AGENTS_RO=OK"`,
			`(dd if=/dev/zero of=/dev/vda bs=512 count=1 2>/dev/null && echo "VDA_RO=FAIL") || echo "VDA_RO=OK"`,
			`(dd if=/dev/zero of=/dev/vdb bs=512 count=1 2>/dev/null && echo "VDB_RO=FAIL") || echo "VDB_RO=OK"`,
			`(dd if=/dev/zero of=/dev/vdc bs=512 count=1 2>/dev/null && echo "VDC_RO=FAIL") || echo "VDC_RO=OK"`,
			`(test ! -e /dev/fuse && test ! -e /dev/kvm && echo "DEVS=OK") || echo "DEVS=FAIL"`,
			`(touch /etc/agent-nope 2>/dev/null && echo "ETC_RO=FAIL") || echo "ETC_RO=OK"`,
			fmt.Sprintf(`(ls /run/%s/sys 2>/dev/null && echo "SYS_DIR_EACCES=FAIL") || echo "SYS_DIR_EACCES=OK"`, brand.Name),
			fmt.Sprintf(`(ls /run/%s/sys/session 2>/dev/null && echo "SESS_DIR_EACCES=FAIL") || echo "SESS_DIR_EACCES=OK"`, brand.Name),
			fmt.Sprintf(`(ls /run/%s/sys/overlay 2>/dev/null && echo "OV_DIR_EACCES=FAIL") || echo "OV_DIR_EACCES=OK"`, brand.Name),
			`echo "AGENT_CGROUP=$(cat /proc/self/cgroup 2>&1)"`,
			`echo "AGENT_OOM_ADJ=$(cat /proc/self/oom_score_adj 2>&1)"`,
			`echo "AGENT_MEM_MAX=$(cat /sys/fs/cgroup/agent/memory.max 2>&1)"`,
			`(grep -q '^oom_kill ' /sys/fs/cgroup/agent/memory.events && echo "AGENT_MEM_EVENTS=OK") || echo "AGENT_MEM_EVENTS=FAIL"`,
			`(echo max > /sys/fs/cgroup/agent/memory.max 2>/dev/null && echo "CGROUP_RO=FAIL") || echo "CGROUP_RO=OK"`,
			`python3 -c '
import ctypes, errno, os, socket
# Find holder PID (uid 900) and verify ptrace(PTRACE_ATTACH) fails with EPERM.
holder_pid = None
for p in os.listdir("/proc"):
    if not p.isdigit():
        continue
    try:
        st = os.stat("/proc/" + p)
        if st.st_uid == 900:
            holder_pid = int(p)
            break
    except OSError:
        pass
print(f"HOLDER_FOUND={holder_pid is not None}")
if holder_pid:
    libc = ctypes.CDLL("libc.so.6", use_errno=True)
    rc = libc.ptrace(16, holder_pid, 0, 0) # PTRACE_ATTACH
    err = ctypes.get_errno()
    print(f"PTRACE_HOLDER_EPERM={rc == -1 and err == errno.EPERM} err={err}")
# Direct UDP must be rejected by guest nftables.
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.connect(("1.1.1.1", 53))
    s.send(b"\x00")
    s.settimeout(0.5)
    try:
        s.recv(16)
        print("UDP_REJECT=FAIL")
    except OSError as e:
        print(f"UDP_REJECT=OK err={e.errno}")
except OSError as e:
    print(f"UDP_REJECT=OK err={e.errno}")
'`,
			`echo "EPHEMERAL_BASHRC" > /home/agent/.bashrc`,
			`echo "PERSIST_MARKER_42" > /work/persist.txt`,
			`echo "CLAUDE_HISTORY_KEPT" > /home/agent/.claude/projects/kept.txt`,
			`sync`,
			`echo "CHECKS_DONE"`,
		}, "; ")

		res1 := bootAndRun(nil, checkScript, nil)
		t.Logf("guest output (boot 1):\n%s", res1.output)
		if !strings.HasPrefix(res1.vsockReply, "OK ") {
			t.Errorf("unexpected vsock reply %q", res1.vsockReply)
		}
		for _, want := range []string{
			"KERNEL=6.18.62",
			"DISTRO=debian",
			"ID=1000:1000:agent",
			"SERIALS= vda=tools vdb=image vdc=agents vdd=session",
			"RESOLV=nameserver 127.0.0.53",
			"AGENT_PATH_FIRST=OK",
			"AGENT_RUN=AGENTS-DISK-OK",
			"AGENTS_RO=OK",
			"VDA_RO=OK",
			"VDB_RO=OK",
			"VDC_RO=OK",
			"DEVS=OK",
			"ETC_RO=OK",
			"SYS_DIR_EACCES=OK",
			"SESS_DIR_EACCES=OK",
			"OV_DIR_EACCES=OK",
			"AGENT_CGROUP=0::/agent",
			"AGENT_OOM_ADJ=500",
			"AGENT_MEM_EVENTS=OK",
			"CGROUP_RO=OK",
			"HOLDER_FOUND=True",
			"PTRACE_HOLDER_EPERM=True",
			"UDP_REJECT=OK",
			"CHECKS_DONE",
		} {
			if !strings.Contains(res1.output, want) {
				t.Errorf("guest output missing %q", want)
			}
		}
		// Ensure only lo and dummy0 (plus optional dummy0p veth peer) exist.
		if !strings.Contains(res1.output, "NET=dummy0,dummy0p,lo,") && !strings.Contains(res1.output, "NET=dummy0,lo,") {
			t.Errorf("unexpected network interfaces in guest output:\n%s", res1.output)
		}
		// Ensure /sys/fs/cgroup/agent/memory.max is a positive integer cap below 512 MiB.
		if idx := strings.Index(res1.output, "AGENT_MEM_MAX="); idx >= 0 {
			rest := res1.output[idx+len("AGENT_MEM_MAX="):]
			line, _, _ := strings.Cut(rest, "\n")
			line = strings.TrimSpace(line)
			n, err := strconv.ParseInt(line, 10, 64)
			if err != nil || n <= 0 || n >= 512<<20 {
				t.Errorf("AGENT_MEM_MAX = %q, want numeric cap in (0, 512MiB)", line)
			}
		} else {
			t.Errorf("missing AGENT_MEM_MAX in guest output")
		}

		// Boot 2 on the same session disk: ~/.bashrc must be GONE, while /work and ~/.claude/projects STAY.
		res2 := bootAndRun(nil, strings.Join([]string{
			`(test ! -e /home/agent/.bashrc && echo "BASHRC_RESET=OK") || echo "BASHRC_RESET=FAIL"`,
			`echo "PERSIST=$(cat /work/persist.txt 2>&1)"`,
			`echo "STATE_DIR=$(cat /home/agent/.claude/projects/kept.txt 2>&1)"`,
			`echo "REBOOT_DONE"`,
		}, "; "), nil)
		t.Logf("guest output (boot 2):\n%s", res2.output)
		for _, want := range []string{
			"BASHRC_RESET=OK",
			"PERSIST=PERSIST_MARKER_42",
			"STATE_DIR=CLAUDE_HISTORY_KEPT",
			"REBOOT_DONE",
		} {
			if !strings.Contains(res2.output, want) {
				t.Errorf("boot 2 output missing %q", want)
			}
		}
	})

	t.Run("transparent proxy synthetic DNS keep-system and sudo", func(t *testing.T) {
		dir := t.TempDir()
		sessionDisk := filepath.Join(dir, "keep-session.img")

		// Local HTTP server reachable ONLY through our mock egress server's
		// synthetic DNS + transparent TCP handler (no real internet needed).
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("TRANSPARENT-SYNTHETIC-OK:" + r.URL.Path))
		}))
		defer upstream.Close()
		upHost, upPortStr, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
		if err != nil {
			t.Fatalf("parse upstream URL: %v", err)
		}
		upAddr := netip.MustParseAddr(upHost)
		upPort, _ := strconv.Atoi(upPortStr)

		policyTOML := fmt.Sprintf("[sandbox]\npreset = \"locked\"\n\n[network]\nunknown = \"deny\"\nallow = [\"allowed.example:%d\"]\n", upPort)
		cfg, err := egress.ParsePolicy([]byte(policyTOML))
		if err != nil {
			t.Fatalf("ParsePolicy: %v", err)
		}
		eng, err := egress.NewEngine(cfg)
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		egSrv, err := egress.NewServer(egress.ServerOptions{
			Engine:  eng,
			DNSMode: egress.DNSModeSynthetic,
			Dialer: &egress.SafeDialer{
				Engine:   eng,
				Addrs:    &egress.AddressPolicy{},
				Resolver: staticAddrResolver{addr: upAddr},
				Dial:     (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
		})
		if err != nil {
			t.Fatalf("egress.NewServer: %v", err)
		}
		defer egSrv.Close()

		bootKeep := func(cmdLine string) guestRunResult {
			sb := chv.NewSandbox(chv.SandboxOptions{
				Config: chv.VMConfig{
					VMMPath:         rp.VMM,
					CHRemotePath:    rp.CHRemote,
					APISocket:       filepath.Join(dir, "vm.vmm"),
					VsockSocket:     filepath.Join(dir, "vm.vsock"),
					KernelPath:      rp.Kernel,
					CPUs:            2,
					MemoryBytes:     512 << 20,
					Tools:           toolsDisk,
					Image:           rootfsDisk,
					AgentsDiskPath:  agentsDisk,
					SessionDiskPath: sessionDisk,
					SessionDirect:   true,
					InitArgs:        []string{"wall-init", "--keep-system", "--sudo"},
				},
				SessionDiskBytes: 64 << 20,
				BwrapPath:        bwrapPath,
				BootTimeout:      45 * time.Second,
			})
			conn, err := sb.Start(ctx)
			if err != nil {
				t.Fatalf("Start keep-system microvm: %v\nstderr:\n%s", err, sb.Stderr())
			}
			defer func() {
				_ = conn.Close()
				_ = sb.Kill()
				_ = sb.Destroy()
			}()
			res := runGuestCommandOverLink(ctx, t, conn, cmdLine, egSrv)
			if err := sb.Stop(ctx, 15*time.Second); err != nil {
				t.Fatalf("Stop keep-system microvm: %v", err)
			}
			if ex := sb.Wait(); ex.Code != 0 || ex.Err != nil {
				t.Fatalf("keep-system clean exit failed: %+v", ex)
			}
			_ = sb.Destroy()
			return res
		}

		// Boot 1: test synthetic DNS, transparent HTTP WITHOUT HTTP_PROXY,
		// blocked host NXDOMAIN, and fresh /home/agent tmpfs.
		script1 := fmt.Sprintf(`
python3 -c '
import os, socket, urllib.request
# Synthetic DNS: allowed.example -> 198.18.x.x, blocked.example -> NXDOMAIN.
ip = socket.gethostbyname("allowed.example")
print("SYNTH_IP=" + ip)
print("IS_SYNTH=" + str(ip.startswith("198.18.") or ip.startswith("198.19.")))
try:
    socket.gethostbyname("blocked.example")
    print("BLOCKED_DNS=FAIL")
except socket.gaierror:
    print("BLOCKED_DNS=OK")

# Transparent TCP proxy: NO HTTP_PROXY set!
for k in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
    os.environ.pop(k, None)
with urllib.request.urlopen("http://allowed.example:%d/ping", timeout=10) as resp:
    print("TRANSPARENT_BODY=" + resp.read().decode())
'
echo "HOME_STILL_RESETS" > /home/agent/.bashrc
(ls /run/%s/sys/session/overlay/upper/etc 2>/dev/null && echo "KEEP_OV_EACCES=FAIL") || echo "KEEP_OV_EACCES=OK"
sync
echo "KEEP_BOOT1_DONE"
`, upPort, brand.Name)

		r1 := bootKeep(script1)
		t.Logf("keep-system + transparent boot 1 (memTotal=%d):\n%s", r1.memTotal, r1.output)
		if r1.memTotal <= 0 {
			t.Errorf("expected ctl mem claim with MemTotal > 0, got %d", r1.memTotal)
		}
		for _, want := range []string{
			"IS_SYNTH=True",
			"BLOCKED_DNS=OK",
			"TRANSPARENT_BODY=TRANSPARENT-SYNTHETIC-OK:/ping",
			"KEEP_OV_EACCES=OK",
			"KEEP_BOOT1_DONE",
		} {
			if !strings.Contains(r1.output, want) {
				t.Errorf("boot 1 missing %q", want)
			}
		}

		// Verify /overlay/upper/etc/sudoers.d/agent was written on the session disk
		// with mode 0440 and NOPASSWD rule, then seed /overlay/upper/etc/keep-marker
		// to verify --keep-system keeps rootfs changes across boot 2 while reset mode
		// (boot 3) drops them.
		debugfsBin, err := exec.LookPath("debugfs")
		if err != nil {
			debugfsBin = "/usr/sbin/debugfs"
		}
		e2fsckBin, err := exec.LookPath("e2fsck")
		if err != nil {
			e2fsckBin = "/usr/sbin/e2fsck"
		}
		// Replay the ext4 journal cleanly (under --keep-system, overlayfs pins
		// the upperdir superblock across shutdown) before inspecting/seeding
		// with debugfs -w so boot 2's kernel mount does not overwrite our seed.
		_, _ = exec.CommandContext(ctx, e2fsckBin, "-p", sessionDisk).CombinedOutput()
		statOut, err := exec.CommandContext(ctx, debugfsBin, "-R", "stat /overlay/upper/etc/sudoers.d/agent", sessionDisk).CombinedOutput()
		if err != nil || !strings.Contains(string(statOut), "0440") {
			t.Fatalf("debugfs stat sudoers.d/agent failed (err=%v):\n%s", err, statOut)
		}
		catOut, err := exec.CommandContext(ctx, debugfsBin, "-R", "cat /overlay/upper/etc/sudoers.d/agent", sessionDisk).CombinedOutput()
		if err != nil || !strings.Contains(string(catOut), "agent ALL=(ALL:ALL) NOPASSWD: ALL") {
			t.Fatalf("debugfs cat sudoers.d/agent failed (err=%v):\n%s", err, catOut)
		}
		markerFile := filepath.Join(dir, "keep-marker")
		if err := os.WriteFile(markerFile, []byte("KEEP_ME_ON_OVERLAY\n"), 0o644); err != nil {
			t.Fatalf("write marker file: %v", err)
		}
		if out, err := exec.CommandContext(ctx, debugfsBin, "-w", "-R", fmt.Sprintf("write %s /overlay/upper/etc/keep-marker", markerFile), sessionDisk).CombinedOutput(); err != nil {
			t.Fatalf("debugfs write keep-marker failed (%v):\n%s", err, out)
		}

		// Boot 2 (--keep-system): /etc/keep-marker STAYS while /home/agent/.bashrc RESETS.
		r2 := bootKeep(`echo "KEPT_OVERLAY=$(cat /etc/keep-marker 2>&1)"; (test ! -e /home/agent/.bashrc && echo "HOME_RESET_WITH_KEEP=OK") || echo "HOME_RESET_WITH_KEEP=FAIL"`)
		t.Logf("keep-system boot 2:\n%s", r2.output)
		if !strings.Contains(r2.output, "KEPT_OVERLAY=KEEP_ME_ON_OVERLAY") {
			t.Errorf("keep-system did not persist overlay rootfs:\n%s", r2.output)
		}
		if !strings.Contains(r2.output, "HOME_RESET_WITH_KEEP=OK") {
			t.Errorf("/home/agent/.bashrc unexpectedly persisted under --keep-system:\n%s", r2.output)
		}

		// Boot 3 (default reset mode on same sessionDisk): /etc/keep-marker is GONE!
		sbReset := chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:         rp.VMM,
				CHRemotePath:    rp.CHRemote,
				APISocket:       filepath.Join(dir, "vm.vmm"),
				VsockSocket:     filepath.Join(dir, "vm.vsock"),
				KernelPath:      rp.Kernel,
				CPUs:            2,
				MemoryBytes:     512 << 20,
				Tools:           toolsDisk,
				Image:           rootfsDisk,
				AgentsDiskPath:  agentsDisk,
				SessionDiskPath: sessionDisk,
				SessionDirect:   true,
			},
			BwrapPath:   bwrapPath,
			BootTimeout: 45 * time.Second,
		})
		conn3, err := sbReset.Start(ctx)
		if err != nil {
			t.Fatalf("Start reset boot 3: %v", err)
		}
		defer func() { _ = conn3.Close(); _ = sbReset.Kill(); _ = sbReset.Destroy() }()
		r3 := runGuestCommandOverLink(ctx, t, conn3, `(test ! -e /etc/keep-marker && echo "ROOT_RESET=OK") || echo "ROOT_RESET=FAIL"`, nil)
		_ = sbReset.Stop(ctx, 15*time.Second)
		sbReset.Wait()
		t.Logf("reset boot 3:\n%s", r3.output)
		if !strings.Contains(r3.output, "ROOT_RESET=OK") {
			t.Errorf("default reset mode unexpectedly exposed keep-system overlay:\n%s", r3.output)
		}
	})

	t.Run("docker flag without dockerd fails with docker_missing", func(t *testing.T) {
		dir := t.TempDir()
		sb := chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:        rp.VMM,
				CHRemotePath:   rp.CHRemote,
				APISocket:      filepath.Join(dir, "d.vmm"),
				VsockSocket:    filepath.Join(dir, "d.vsock"),
				KernelPath:     rp.Kernel,
				CPUs:           1,
				MemoryBytes:    512 << 20,
				Tools:          toolsDisk,
				Image:          rootfsDisk,
				AgentsDiskPath: agentsDisk,
				InitArgs:       []string{"wall-init", "--docker"},
			},
			BwrapPath:   bwrapPath,
			BootTimeout: 45 * time.Second,
		})
		conn, err := sb.Start(ctx)
		if err != nil {
			t.Fatalf("Start docker microvm: %v\nstderr:\n%s", err, sb.Stderr())
		}
		defer func() {
			_ = conn.Close()
			_ = sb.Kill()
			_ = sb.Destroy()
		}()

		sess := link.New(conn, link.Supervisor, link.Options{})
		defer sess.Close()
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		ctl, err := sess.Open(cctx, link.KindCtl, nil)
		if err != nil {
			t.Fatalf("open ctl: %v", err)
		}
		defer ctl.Close()
		w := proto.NewWriter(ctl)
		rd := bufio.NewReader(ctl)
		reqArgs, _ := json.Marshal(map[string]any{
			"session": "dock1", "name": "dock1", "agent": "sh",
			"argv": []string{"/bin/sh", "-c", "id"}, "cwd": "/work",
		})
		if err := w.WriteJSONCtx(cctx, proto.TRequest, proto.Request{ID: 1, Op: "start", Args: reqArgs}); err != nil {
			t.Fatalf("send start: %v", err)
		}
		for {
			f, err := proto.Read(rd)
			if err != nil {
				t.Fatalf("read ctl: %v", err)
			}
			if f.Type != proto.TResponse {
				continue
			}
			var resp proto.Response
			if err := json.Unmarshal(f.Payload, &resp); err != nil {
				t.Fatalf("decode resp: %v", err)
			}
			if resp.OK || resp.Error == nil || resp.Error.Code != "docker_missing" || !strings.Contains(resp.Error.Hint, "--image") {
				t.Fatalf("got start resp %+v, want docker_missing with --image hint", resp)
			}
			t.Logf("docker_missing verified: %+v", resp.Error)
			break
		}
		if err := sb.Stop(ctx, 15*time.Second); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		sb.Wait()
	})

	t.Run("qcow2 header on session disk is treated as raw", func(t *testing.T) {
		dir := t.TempDir()
		qcowDisk := filepath.Join(dir, "qcow.img")
		if _, err := chv.EnsureSessionDisk(ctx, qcowDisk, 32<<20, ""); err != nil {
			t.Fatalf("EnsureSessionDisk: %v", err)
		}
		hdr := make([]byte, 512)
		copy(hdr[0:4], "QFI\xfb")
		binary.BigEndian.PutUint32(hdr[4:8], 3)
		binary.BigEndian.PutUint64(hdr[8:16], 128)
		binary.BigEndian.PutUint32(hdr[16:20], 11)
		binary.BigEndian.PutUint32(hdr[20:24], 16)
		binary.BigEndian.PutUint64(hdr[24:32], 1<<20)
		copy(hdr[128:], "/etc/passwd")
		qf, err := os.OpenFile(qcowDisk, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("open qcow disk: %v", err)
		}
		if _, err := qf.WriteAt(hdr, 0); err != nil {
			t.Fatalf("write qcow header into ext4 boot sector: %v", err)
		}
		_ = qf.Close()

		sb2 := chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:         rp.VMM,
				CHRemotePath:    rp.CHRemote,
				APISocket:       filepath.Join(dir, "q2.vmm"),
				VsockSocket:     filepath.Join(dir, "q2.vsock"),
				KernelPath:      rp.Kernel,
				CPUs:            1,
				MemoryBytes:     512 << 20,
				Tools:           toolsDisk,
				Image:           rootfsDisk,
				AgentsDiskPath:  agentsDisk,
				SessionDiskPath: qcowDisk,
				SessionDirect:   true,
				InitArgs:        []string{"wall-init", "--sudo"},
			},
			BwrapPath:   bwrapPath,
			BootTimeout: 45 * time.Second,
		})
		conn, err := sb2.Start(ctx)
		if err != nil {
			t.Fatalf("Start with QCOW2 header in session disk: %v\nstderr:\n%s", err, sb2.Stderr())
		}
		defer func() {
			_ = conn.Close()
			_ = sb2.Kill()
			_ = sb2.Destroy()
		}()
		res := runGuestCommandOverLink(ctx, t, conn, `head -c 4 /dev/vdd 2>/dev/null | od -An -tx1 || echo "RAW_BLOCK_DENIED_TO_AGENT"; (grep -a "root:x:0:0" /dev/vdd >/dev/null 2>&1 && echo "HOST_PASSWD_LEAK") || echo "NO_BACKING_FILE_LEAK"`, nil)
		t.Logf("qcow2 test output:\n%s", res.output)
		if !strings.Contains(res.output, "NO_BACKING_FILE_LEAK") {
			t.Fatalf("QCOW2 raw disk check failed:\n%s", res.output)
		}
		_ = sb2.Stop(ctx, 15*time.Second)
		sb2.Wait()
	})

	t.Run("tampered tools byte stops boot via dm-verity", func(t *testing.T) {
		dir := t.TempDir()
		badTools := filepath.Join(dir, "tampered-tools.erofs")
		raw, err := os.ReadFile(toolsDisk.Path)
		if err != nil {
			t.Fatalf("read tools image: %v", err)
		}
		raw[1024] ^= 0x01
		if err := os.WriteFile(badTools, raw, 0o444); err != nil {
			t.Fatalf("write tampered tools: %v", err)
		}
		bad := toolsDisk
		bad.Path = badTools
		sb := chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:       rp.VMM,
				CHRemotePath:  rp.CHRemote,
				APISocket:     filepath.Join(dir, "bad.vmm"),
				VsockSocket:   filepath.Join(dir, "bad.vsock"),
				KernelPath:    rp.Kernel,
				CPUs:          1,
				MemoryBytes:   512 << 20,
				Tools:         bad,
				SerialConsole: true,
			},
			BwrapPath:   bwrapPath,
			BootTimeout: 8 * time.Second,
		})
		conn, err := sb.Start(ctx)
		if conn != nil {
			_ = conn.Close()
		}
		defer func() { _ = sb.Destroy() }()
		if err == nil {
			_ = sb.Kill()
			t.Fatal("expected tampered tools image boot to fail under dm-verity")
		}
		if !strings.Contains(err.Error(), "sandbox_boot") {
			t.Errorf("expected sandbox_boot error, got %v", err)
		}
		t.Logf("tampered boot rejected as expected: %v", err)
	})
}

type staticAddrResolver struct{ addr netip.Addr }

func (r staticAddrResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.addr}, nil
}

type guestRunResult struct {
	output     string
	vsockReply string
	memTotal   int64
}

func runGuestCommandOverLink(parent context.Context, t *testing.T, conn net.Conn, shellCmd string, egSrv *egress.Server) guestRunResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	sess := link.New(conn, link.Supervisor, link.Options{})
	defer sess.Close()

	var egWG sync.WaitGroup
	if egSrv != nil {
		egWG.Add(1)
		go func() {
			defer egWG.Done()
			for {
				st, err := sess.Accept(ctx)
				if err != nil {
					return
				}
				switch st.Kind() {
				case link.KindDNS:
					egWG.Add(1)
					go func(s *link.Stream) {
						defer egWG.Done()
						defer s.Close()
						egSrv.DNS.ServeStream(ctx, s)
					}(st)
				case link.KindEgress:
					egWG.Add(1)
					go func(s *link.Stream) {
						defer egWG.Done()
						defer s.Close()
						var hdr egress.StreamHeader
						if err := s.Header(&hdr); err != nil {
							return
						}
						if hdr.Via == "transparent" {
							egSrv.HandleTransparent(ctx, s, hdr.Host, hdr.Port)
						}
					}(st)
				default:
					_ = st.Close()
				}
			}
		}()
	}

	ctl, err := sess.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatalf("open ctl stream: %v", err)
	}
	defer ctl.Close()

	ctlW := proto.NewWriter(ctl)
	ctlR := bufio.NewReader(ctl)

	reqArgs, _ := json.Marshal(map[string]any{
		"session": "e2e1",
		"name":    "e2e1",
		"agent":   "sh",
		"argv":    []string{"/bin/sh", "-c", shellCmd},
		"env":     []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent", "USER=agent", "TERM=xterm-256color"},
		"cwd":     "/work",
		"term":    "xterm-256color",
		"cols":    120,
		"rows":    40,
	})
	if err := ctlW.WriteJSONCtx(ctx, proto.TRequest, proto.Request{ID: 1, Op: "start", Args: reqArgs}); err != nil {
		t.Fatalf("send start ctl: %v", err)
	}

	var memTotal int64
	for {
		f, err := proto.Read(ctlR)
		if err != nil {
			t.Fatalf("read ctl start response: %v", err)
		}
		if f.Type == proto.TEvent {
			var ev struct {
				Type     string `json:"type"`
				MemTotal int64  `json:"mem_total"`
			}
			if json.Unmarshal(f.Payload, &ev) == nil && ev.Type == "mem" && ev.MemTotal > 0 {
				memTotal = ev.MemTotal
			}
			continue
		}
		if f.Type != proto.TResponse {
			continue
		}
		var resp proto.Response
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			t.Fatalf("decode ctl response: %v", err)
		}
		if !resp.OK {
			t.Fatalf("guest start failed: %+v", resp.Error)
		}
		break
	}

	hst, err := sess.Open(ctx, link.KindHolder, map[string]string{"session": "e2e1"})
	if err != nil {
		t.Fatalf("open holder stream: %v", err)
	}
	defer hst.Close()

	hw := proto.NewWriter(hst)
	hr := bufio.NewReader(hst)
	if err := hw.WriteJSONCtx(ctx, proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "client",
	}); err != nil {
		t.Fatalf("send holder hello: %v", err)
	}
	hf, err := proto.Read(hr)
	if err != nil || hf.Type != proto.THello {
		t.Fatalf("read holder hello: frame=%v err=%v", hf.Type, err)
	}
	if err := hw.WriteJSONCtx(ctx, proto.TAttach, proto.Attach{
		LastOffset: 0,
		Cols:       120,
		Rows:       40,
	}); err != nil {
		t.Fatalf("send attach: %v", err)
	}

	var out strings.Builder
	for {
		f, err := proto.Read(hr)
		if err != nil {
			break
		}
		switch f.Type {
		case proto.TOutput:
			if _, data, derr := proto.DecodeOutput(f.Payload); derr == nil {
				out.Write(data)
			}
		case proto.TEnded:
			cancel()
			_ = sess.Close()
			egWG.Wait()
			return guestRunResult{output: out.String(), memTotal: memTotal}
		default:
		}
	}
	cancel()
	_ = sess.Close()
	egWG.Wait()
	return guestRunResult{output: out.String(), memTotal: memTotal}
}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("missing required env %s", key)
	}
	return v
}

func mustEnvUint64(t *testing.T, key string) uint64 {
	t.Helper()
	v := mustEnv(t, key)
	var n uint64
	for i := range len(v) {
		c := v[i]
		if c < '0' || c > '9' {
			t.Fatalf("invalid uint64 %s=%q", key, v)
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

// TestBuildToolsFixture builds a dm-verity EROFS tools image when
// CHV_BUILD_TOOLS_HELPER and CHV_BUILD_TOOLS_OUT are set (used to stage
// tools.erofs from a build machine onto a KVM test host).
func TestBuildToolsFixture(t *testing.T) {
	helper := os.Getenv("CHV_BUILD_TOOLS_HELPER")
	out := os.Getenv("CHV_BUILD_TOOLS_OUT")
	if helper == "" || out == "" {
		t.Skip("set CHV_BUILD_TOOLS_HELPER and CHV_BUILD_TOOLS_OUT to build a tools fixture")
	}
	rec, err := chv.BuildToolsImage(context.Background(), helper, out, chv.ToolsBuildOptions{})
	if err != nil {
		t.Fatalf("BuildToolsImage: %v", err)
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(out+".json", append(b, '\n'), 0o600); err != nil {
		t.Fatalf("write tools json: %v", err)
	}
	t.Logf("built tools image: %s", string(b))
}
