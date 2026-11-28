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

// TestCloudHypervisorAgentsDiskLiveKVM boots a real Cloud Hypervisor microvm
// on a Linux KVM host with an agents disk built from the host's real installed
// agents (Claude Code and Codex), verifies both start inside the guest with no
// prompt sent, and verifies writes under /opt/agents fail with EROFS (spec 20.4).
// Skipped unless CHV_E2E_AGENTS_LIVE=1 is set.
func TestCloudHypervisorAgentsDiskLiveKVM(t *testing.T) {
	if os.Getenv("CHV_E2E_AGENTS_LIVE") != "1" {
		t.Skip("set CHV_E2E_AGENTS_LIVE=1 on a KVM host to run live agents-disk microvm tests")
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
	dir := t.TempDir()
	sessionDisk := filepath.Join(dir, "session.img")

	sb := chv.NewSandbox(chv.SandboxOptions{
		Config: chv.VMConfig{
			VMMPath:         rp.VMM,
			CHRemotePath:    rp.CHRemote,
			APISocket:       filepath.Join(dir, "vm.vmm"),
			VsockSocket:     filepath.Join(dir, "vm.vsock"),
			KernelPath:      rp.Kernel,
			CPUs:            2,
			MemoryBytes:     2 << 30, // 2 GiB so standalone Claude Code and Codex binaries start cleanly
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

	checkScript := strings.Join([]string{
		`echo "AGENTS_MOUNT=$(grep ' /opt/agents ' /proc/mounts | head -n 1)"`,
		`echo "CLAUDE_LINK=$(readlink /opt/agents/bin/claude)"`,
		`echo "CODEX_LINK=$(readlink /opt/agents/bin/codex)"`,
		`echo "CLAUDE_UIDGID=$(stat -c %u:%g /opt/agents/bin/claude)"`,
		`echo "CODEX_UIDGID=$(stat -c %u:%g /opt/agents/bin/codex)"`,
		`err1=$(touch /opt/agents/write-probe 2>&1); rc1=$?; echo "WRITE_DIR_RC=$rc1 ERR=$err1"`,
		`err2=$(sh -c 'echo x >> /opt/agents/bin/claude' 2>&1); rc2=$?; echo "WRITE_BIN_RC=$rc2 ERR=$err2"`,
		`cver=$(claude --version 2>&1); crc=$?; echo "CLAUDE_VER_RC=$crc OUT=$cver"`,
		`claude --help >/dev/null 2>&1; echo "CLAUDE_HELP_RC=$?"`,
		`xver=$(codex --version 2>&1); xrc=$?; echo "CODEX_VER_RC=$xrc OUT=$xver"`,
		`codex --help >/dev/null 2>&1; echo "CODEX_HELP_RC=$?"`,
		`echo "AGENTS_LIVE_DONE"`,
	}, "; ")

	out := runAgentsGuestCmd(ctx, t, conn, checkScript)
	t.Logf("guest agents-disk output:\n%s", out)

	for _, want := range []string{
		"CLAUDE_LINK=../root/",
		"CODEX_LINK=../root/",
		"CLAUDE_UIDGID=1000:1000",
		"CODEX_UIDGID=1000:1000",
		"Read-only file system",
		"CLAUDE_VER_RC=0",
		"Claude Code",
		"CLAUDE_HELP_RC=0",
		"CODEX_VER_RC=0",
		"codex-cli",
		"CODEX_HELP_RC=0",
		"AGENTS_LIVE_DONE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guest output missing %q", want)
		}
	}
	if strings.Contains(out, "WRITE_DIR_RC=0") || strings.Contains(out, "WRITE_BIN_RC=0") {
		t.Errorf("write under /opt/agents unexpectedly succeeded:\n%s", out)
	}

	if err := sb.Stop(ctx, 15*time.Second); err != nil {
		t.Fatalf("Stop via ch-remote power-button: %v", err)
	}
	ex := sb.Wait()
	if ex.Code != 0 || ex.Err != nil {
		t.Fatalf("expected clean exit 0 after power-button, got %+v\nstderr:\n%s", ex, sb.Stderr())
	}
}

func runAgentsGuestCmd(parent context.Context, t *testing.T, conn net.Conn, shellCmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
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
		"session": "agents1",
		"name":    "agents1",
		"agent":   "sh",
		"argv":    []string{"/bin/sh", "-c", shellCmd},
		"env": []string{
			"PATH=/opt/agents/bin:/usr/local/bin:/usr/bin:/bin",
			"HOME=/work",
			"TERM=xterm-256color",
			"DISABLE_AUTOUPDATER=1",
			"NO_UPDATE_NOTIFIER=1",
		},
		"cwd":  "/work",
		"term": "xterm-256color",
		"cols": 120,
		"rows": 40,
	})
	if err := ctlW.WriteJSONCtx(ctx, proto.TRequest, proto.Request{ID: 1, Op: "start", Args: reqArgs}); err != nil {
		t.Fatalf("send start ctl: %v", err)
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

	hst, err := sess.Open(ctx, link.KindHolder, map[string]string{"session": "agents1"})
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
