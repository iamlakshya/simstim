//go:build linux

package chv_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	t.Run("full microvm boot checks power-button and reboot", func(t *testing.T) {
		dir := t.TempDir()
		sessionDisk := filepath.Join(dir, "session.img")

		bootAndRun := func(cmdLine string) (output string, vsockReply string) {
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

			out := runGuestCommandOverLink(ctx, t, conn, cmdLine)

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
			return out, sb.VsockReply()
		}

		checkScript := strings.Join([]string{
			`echo "KERNEL=$(uname -r)"`,
			`echo "DISTRO=$(grep '^ID=' /etc/os-release | cut -d= -f2)"`,
			`SERIALS=""; for d in vda vdb vdc vdd; do s=$(tr -d '\0\n ' </sys/block/$d/serial 2>/dev/null || echo missing); SERIALS="$SERIALS $d=$s"; done; echo "SERIALS=$SERIALS"`,
			`echo "NET=$(ls /sys/class/net | tr '\n' ',')"`,
			`echo "AGENT_RUN=$(/opt/agents/bin/probe-agent 2>&1)"`,
			`(touch /opt/agents/ro-test 2>&1 && echo "AGENTS_RO=FAIL") || echo "AGENTS_RO=OK"`,
			`(dd if=/dev/zero of=/dev/vda bs=512 count=1 2>/dev/null && echo "VDA_RO=FAIL") || echo "VDA_RO=OK"`,
			`(dd if=/dev/zero of=/dev/vdb bs=512 count=1 2>/dev/null && echo "VDB_RO=FAIL") || echo "VDB_RO=OK"`,
			`(dd if=/dev/zero of=/dev/vdc bs=512 count=1 2>/dev/null && echo "VDC_RO=FAIL") || echo "VDC_RO=OK"`,
			`(dd if=/dev/zero of=/work/direct512 bs=512 count=4 oflag=direct 2>/dev/null && echo "DIRECT512=OK") || echo "DIRECT512=FAIL"`,
			`echo "PERSIST_MARKER_42" > /work/persist.txt && sync`,
			`echo "CHECKS_DONE"`,
		}, "; ")

		out, reply := bootAndRun(checkScript)
		t.Logf("guest output (boot 1):\n%s", out)
		if !strings.HasPrefix(reply, "OK ") {
			t.Errorf("unexpected vsock reply %q", reply)
		}
		for _, want := range []string{
			"KERNEL=6.18.62",
			"DISTRO=debian",
			"SERIALS= vda=tools vdb=image vdc=agents vdd=session",
			"NET=lo,",
			"AGENT_RUN=AGENTS-DISK-OK",
			"AGENTS_RO=OK",
			"VDA_RO=OK",
			"VDB_RO=OK",
			"VDC_RO=OK",
			"CHECKS_DONE",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("guest output missing %q", want)
			}
		}

		// Boot 2 on the same session disk: verify persistence and never-reformat.
		out2, _ := bootAndRun(`echo "PERSIST=$(cat /work/persist.txt 2>&1)"; echo "REBOOT_DONE"`)
		t.Logf("guest output (boot 2):\n%s", out2)
		if !strings.Contains(out2, "PERSIST=PERSIST_MARKER_42") {
			t.Errorf("session disk did not persist across reboot:\n%s", out2)
		}
	})

	t.Run("qcow2 header on session disk is treated as raw", func(t *testing.T) {
		dir := t.TempDir()
		qcowDisk := filepath.Join(dir, "qcow.img")
		if _, err := chv.EnsureSessionDisk(ctx, qcowDisk, 32<<20, ""); err != nil {
			t.Fatalf("EnsureSessionDisk: %v", err)
		}
		// Write valid QCOW2 v3 header pointing backing_file to /etc/passwd into
		// bytes 0..511 (ext4 leaves bytes 0..1023 unused as a boot sector, so
		// the session disk still mounts cleanly while carrying QCOW2 magic).
		hdr := make([]byte, 512)
		copy(hdr[0:4], "QFI\xfb")
		binary.BigEndian.PutUint32(hdr[4:8], 3)       // version 3
		binary.BigEndian.PutUint64(hdr[8:16], 128)    // backing_file_offset
		binary.BigEndian.PutUint32(hdr[16:20], 11)    // len("/etc/passwd")
		binary.BigEndian.PutUint32(hdr[20:24], 16)    // cluster_bits
		binary.BigEndian.PutUint64(hdr[24:32], 1<<20) // virtual size 1 MiB
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
		out := runGuestCommandOverLink(ctx, t, conn, `head -c 4 /dev/vdd | od -An -tx1; (grep -a "root:x:0:0" /dev/vdd >/dev/null 2>&1 && echo "HOST_PASSWD_LEAK") || echo "NO_BACKING_FILE_LEAK"`)
		t.Logf("qcow2 test output:\n%s", out)
		if !strings.Contains(out, "51 46 49 fb") || !strings.Contains(out, "NO_BACKING_FILE_LEAK") {
			t.Fatalf("QCOW2 raw disk check failed:\n%s", out)
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
		// Flip 1 bit in block 0 (EROFS data) while keeping the original RootHash.
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

func runGuestCommandOverLink(parent context.Context, t *testing.T, conn net.Conn, shellCmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()

	sess := link.New(conn, link.Supervisor, link.Options{})
	defer sess.Close()

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
		"env":     []string{"PATH=/opt/agents/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/work", "TERM=xterm-256color"},
		"cwd":     "/work",
		"term":    "xterm-256color",
		"cols":    120,
		"rows":    40,
	})
	if err := ctlW.WriteJSONCtx(ctx, proto.TRequest, proto.Request{ID: 1, Op: "start", Args: reqArgs}); err != nil {
		t.Fatalf("send start ctl: %v", err)
	}

	// Read start response (skipping any early events).
	for {
		f, err := proto.Read(ctlR)
		if err != nil {
			t.Fatalf("read ctl start response: %v", err)
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

	// Attach to the holder stream using the real holder protocol handshake.
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
			return out.String()
		default:
		}
	}
	return out.String()
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
