//go:build linux

package chv_test

import (
	"bufio"
	"context"
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

// TestCloudHypervisorLifecycleLiveKVM proves P3.6 on a Linux KVM host (host-a):
//   - Small sparse ext4 session disk (32 MiB cap, unencrypted): filling /work
//     returns ENOSPC to the guest and host allocated space (du / st_blocks*512)
//     stays at or below disk_max.
//   - Clean stop via ch-remote power-button exits Cloud Hypervisor cleanly with
//     no VMM process or sockets left behind, preserves disk.img without saving
//     a memory snapshot, and a second cold boot wakes cleanly with persisted
//     /work files intact.
//   - Driver.Open inspects the running microvm over its VMM API socket.
//
// Skipped unless CHV_E2E_LIFECYCLE=1 is set.
func TestCloudHypervisorLifecycleLiveKVM(t *testing.T) {
	if os.Getenv("CHV_E2E_LIFECYCLE") != "1" {
		t.Skip("set CHV_E2E_LIFECYCLE=1 on a KVM host to run live microvm lifecycle tests")
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
	bwrapPath := os.Getenv("CHV_E2E_BWRAP")

	ctx := context.Background()
	dir := t.TempDir()
	sessionDisk := filepath.Join(dir, "disk.img")
	const diskCap = 32 << 20 // 32 MiB hard sparse ext4 cap

	bootVM := func(cmd string) string {
		apiSock := filepath.Join(dir, "vm.vmm")
		vsockSock := filepath.Join(dir, "vm.vsock")
		sb := chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:         rp.VMM,
				CHRemotePath:    rp.CHRemote,
				APISocket:       apiSock,
				VsockSocket:     vsockSock,
				KernelPath:      rp.Kernel,
				CPUs:            2,
				MemoryBytes:     512 << 20,
				Tools:           toolsDisk,
				Image:           rootfsDisk,
				SessionDiskPath: sessionDisk,
				SessionDirect:   true,
			},
			SessionDiskBytes: diskCap,
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

		out := runLifecycleGuestCmd(ctx, t, conn, cmd)

		if err := sb.Stop(ctx, 15*time.Second); err != nil {
			t.Fatalf("Stop via ch-remote power-button: %v", err)
		}
		ex := sb.Wait()
		if ex.Code != 0 || ex.Err != nil {
			t.Fatalf("expected exit 0 after power-button, got %+v\nstderr:\n%s", ex, sb.Stderr())
		}
		if err := sb.Destroy(); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		for _, sock := range []string{apiSock, vsockSock} {
			if _, err := os.Stat(sock); !os.IsNotExist(err) {
				t.Fatalf("socket left after stop/destroy: %s", sock)
			}
		}
		return out
	}

	// Boot 1: write a persistence marker, then fill /work until ENOSPC.
	out1 := bootVM(strings.Join([]string{
		`echo "SURVIVE_P36" > /work/marker.txt`,
		`dd_err=$(dd if=/dev/zero of=/work/fill.bin bs=1M count=64 2>&1); rc=$?`,
		`echo "FILL_RC=$rc"`,
		`echo "FILL_ERR=$dd_err"`,
		`sync`,
	}, "; "))
	t.Logf("boot 1 output:\n%s", out1)
	if !strings.Contains(out1, "No space left on device") {
		t.Fatalf("expected ENOSPC filling 32 MiB session disk, got:\n%s", out1)
	}
	if strings.Contains(out1, "FILL_RC=0") {
		t.Fatalf("64 MiB write into 32 MiB disk unexpectedly succeeded:\n%s", out1)
	}

	// Verify host allocated space (du / st_blocks*512) stays <= diskCap (32 MiB).
	alloc, apparent, err := chv.VerifySessionDiskCap(sessionDisk, diskCap)
	if err != nil {
		t.Fatalf("VerifySessionDiskCap after ENOSPC: %v", err)
	}
	t.Logf("full session disk on host: allocated=%d bytes apparent=%d bytes cap=%d bytes", alloc, apparent, diskCap)

	// Boot 2 (wake from stopped state): remove fill.bin and confirm marker survived
	// cleanly on the unencrypted ext4 disk across cold stop and wake.
	out2 := bootVM(`rm -f /work/fill.bin && echo "WOKE_MARKER=$(cat /work/marker.txt)"`)
	t.Logf("boot 2 (wake) output:\n%s", out2)
	if !strings.Contains(out2, "WOKE_MARKER=SURVIVE_P36") {
		t.Fatalf("marker did not persist across stop and wake:\n%s", out2)
	}
}

func runLifecycleGuestCmd(parent context.Context, t *testing.T, conn net.Conn, shellCmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
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
		"session": "life1",
		"name":    "life1",
		"agent":   "sh",
		"argv":    []string{"/bin/sh", "-c", shellCmd},
		"env":     []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/work", "TERM=xterm-256color"},
		"cwd":     "/work",
		"term":    "xterm-256color",
		"cols":    120,
		"rows":    40,
	})
	if err := ctlW.WriteJSONCtx(ctx, proto.TRequest, proto.Request{ID: 1, Op: "start", Args: reqArgs}); err != nil {
		t.Fatalf("send start ctl: %v", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = ctl.SetReadDeadline(dl)
	}
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

	hst, err := sess.Open(ctx, link.KindHolder, map[string]string{"session": "life1"})
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
	if dl, ok := ctx.Deadline(); ok {
		_ = hst.SetReadDeadline(dl)
	}
	if hf, err := proto.Read(hr); err != nil || hf.Type != proto.THello {
		t.Fatalf("read holder hello: frame=%v err=%v", hf.Type, err)
	}
	if err := hw.WriteJSONCtx(ctx, proto.TAttach, proto.Attach{LastOffset: 0, Cols: 120, Rows: 40}); err != nil {
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
