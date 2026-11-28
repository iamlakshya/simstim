package chv

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestMemoryMathSpec2010(t *testing.T) {
	// Spec 20.10 examples:
	// - 4 GiB guest -> 128 + 16 = 144 MiB reserve -> 4,240 MiB UnitMemoryMax
	// - 1 GiB guest -> 128 + 4  = 132 MiB reserve -> 1,156 MiB UnitMemoryMax
	cases := []struct {
		guestMiB   int64
		reserveMiB int64
		unitMiB    int64
	}{
		{256, 129, 385},
		{512, 130, 642},
		{1024, 132, 1156},
		{2048, 136, 2184},
		{4096, 144, 4240},
		{8192, 160, 8352},
		// Non-256-MiB multiple rounds page-table reserve up to a whole MiB:
		// 300 MiB / 256 = 1.17 MiB -> 2 MiB + 128 MiB = 130 MiB.
		{300, 130, 430},
	}
	for _, tc := range cases {
		guestBytes := tc.guestMiB << 20
		if got := Reserve(guestBytes); got != tc.reserveMiB<<20 {
			t.Errorf("Reserve(%d MiB) = %d MiB, want %d MiB", tc.guestMiB, got>>20, tc.reserveMiB)
		}
		if got := UnitMemoryMax(guestBytes); got != tc.unitMiB<<20 {
			t.Errorf("UnitMemoryMax(%d MiB) = %d MiB, want %d MiB", tc.guestMiB, got>>20, tc.unitMiB)
		}
		gotMiB, err := GuestMemoryMiB(guestBytes)
		if err != nil || gotMiB != tc.guestMiB {
			t.Errorf("GuestMemoryMiB(%d MiB) = (%d, %v)", tc.guestMiB, gotMiB, err)
		}
	}
	if _, err := GuestMemoryMiB(128 << 20); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("expected error with Try hint for 128 MiB, got %v", err)
	}
}

func TestBuildArgvAndCmdlineGolden(t *testing.T) {
	const (
		toolsHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		imageHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	cfg := VMConfig{
		VMMPath:         "/var/lib/runtime/cloud-hypervisor",
		CHRemotePath:    "/var/lib/runtime/ch-remote",
		APISocket:       "/run/user/1000/v/sess1.vmm",
		VsockSocket:     "/run/user/1000/v/sess1.vsock",
		KernelPath:      "/var/lib/runtime/vmlinux",
		CPUs:            2,
		MemoryBytes:     4 << 30,
		Tools:           DiskVerity{Path: "/var/lib/runtime/tools.erofs", RootHash: toolsHash, DataBlocks: 2048, HasSuperblock: false},
		Image:           &DiskVerity{Path: "/var/lib/images/abc/rootfs.img", RootHash: imageHash, DataBlocks: 97711, HasSuperblock: true},
		AgentsDiskPath:  "/var/lib/images/agents/def.erofs",
		SessionDiskPath: "/var/lib/sessions/sess1/disk.img",
		SessionDirect:   true,
	}

	wantCmdline := `ro quiet root=/dev/dm-0 rootfstype=erofs init=/init dm-mod.waitfor=/dev/vda,/dev/vdb dm-mod.create="tools,,0,ro,0 16384 verity 1 /dev/vda /dev/vda 4096 4096 2048 2048 sha256 ` + toolsHash + ` ` + ZeroSaltHex + ` 1 panic_on_corruption;image,,1,ro,0 781688 verity 1 /dev/vdb /dev/vdb 4096 4096 97711 97712 sha256 ` + imageHash + ` ` + ZeroSaltHex + ` 1 panic_on_corruption" -- wall-init`
	gotCmdline, err := BuildKernelCmdline(cfg)
	if err != nil {
		t.Fatalf("BuildKernelCmdline: %v", err)
	}
	if gotCmdline != wantCmdline {
		t.Fatalf("cmdline mismatch:\ngot:  %s\nwant: %s", gotCmdline, wantCmdline)
	}

	wantArgv := []string{
		"--api-socket", "path=/run/user/1000/v/sess1.vmm",
		"--kernel", "/var/lib/runtime/vmlinux",
		"--cmdline", wantCmdline,
		"--cpus", "boot=2",
		"--memory", "size=4096M",
		"--balloon", "size=0,free_page_reporting=on",
		"--disk",
		"path=/var/lib/runtime/tools.erofs,readonly=on,direct=on,image_type=raw,backing_files=off,serial=tools",
		"path=/var/lib/images/abc/rootfs.img,readonly=on,direct=on,image_type=raw,backing_files=off,serial=image",
		"path=/var/lib/images/agents/def.erofs,readonly=on,direct=on,image_type=raw,backing_files=off,serial=agents",
		"path=/var/lib/sessions/sess1/disk.img,readonly=off,direct=on,image_type=raw,backing_files=off,sparse=on,serial=session",
		"--vsock", "cid=3,socket=/run/user/1000/v/sess1.vsock",
		"--serial", "off",
		"--console", "off",
		"--seccomp", "true",
		"--landlock",
	}
	gotArgv, err := BuildArgv(cfg)
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	if !slices.Equal(gotArgv, wantArgv) {
		t.Fatalf("argv mismatch:\ngot:  %q\nwant: %q", gotArgv, wantArgv)
	}

	// Verify security invariants on every disk and no NIC.
	if slices.Contains(gotArgv, "--net") {
		t.Fatal("argv must not contain --net")
	}
	for _, a := range gotArgv {
		if strings.HasPrefix(a, "path=") && strings.Contains(a, "serial=") {
			if !strings.Contains(a, "image_type=raw") || !strings.Contains(a, "backing_files=off") {
				t.Errorf("disk arg missing raw/backing_files=off: %s", a)
			}
		}
	}

	// Verify bubblewrap confinement wrapper binds artefacts read-only and unshares net.
	wrapped := WrapBwrap(cfg, cfg.VMMPath, gotArgv)
	for _, req := range []string{"--unshare-net", "--die-with-parent", "/dev/kvm", "--ro-bind", cfg.KernelPath} {
		if !slices.Contains(wrapped, req) {
			t.Errorf("WrapBwrap missing %q in %q", req, wrapped)
		}
	}
}

func TestBuildArgvRejectsBadInputs(t *testing.T) {
	valid := VMConfig{
		APISocket:   "/tmp/a.vmm",
		VsockSocket: "/tmp/a.vsock",
		KernelPath:  "/tmp/vmlinux",
		CPUs:        1,
		MemoryBytes: 512 << 20,
		Tools: DiskVerity{
			Path:       "/tmp/tools.erofs",
			RootHash:   strings.Repeat("a", 64),
			DataBlocks: 10,
		},
	}
	badComma := valid
	badComma.KernelPath = "/tmp/vm,linux"
	if _, err := BuildArgv(badComma); err == nil {
		t.Error("expected comma in path to be rejected")
	}
	badHash := valid
	badHash.Tools.RootHash = "not-hex"
	if _, err := BuildArgv(badHash); err == nil {
		t.Error("expected invalid verity root hash to be rejected")
	}
	badInit := valid
	badInit.InitArgs = []string{"wall-init;evil"}
	if _, err := BuildArgv(badInit); err == nil {
		t.Error("expected semicolon in init args to be rejected")
	}
}

func TestEnsureSessionDiskNeverReformatsExisting(t *testing.T) {
	mkfs, err := FindMkfsExt4(nil, nil)
	if err != nil {
		t.Skipf("mkfs.ext4 not available: %v", err)
	}
	dir := t.TempDir()
	diskPath := filepath.Join(dir, "disk.img")

	created, err := EnsureSessionDisk(context.Background(), diskPath, MinSessionDiskBytes, mkfs)
	if err != nil {
		t.Fatalf("first EnsureSessionDisk: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first call")
	}
	fi, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat disk: %v", err)
	}
	if fi.Size() != MinSessionDiskBytes || fi.Mode().Perm() != 0o600 {
		t.Fatalf("disk size=%d mode=%o, want %d 0600", fi.Size(), fi.Mode().Perm(), MinSessionDiskBytes)
	}

	// Overwrite first 16 bytes with a sentinel and verify a second call NEVER reformats it.
	sentinel := []byte("KEEP-MY-DATA-123")
	f, err := os.OpenFile(diskPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open disk: %v", err)
	}
	if _, err := f.WriteAt(sentinel, 0); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	_ = f.Close()

	created2, err := EnsureSessionDisk(context.Background(), diskPath, MinSessionDiskBytes, mkfs)
	if err != nil {
		t.Fatalf("second EnsureSessionDisk: %v", err)
	}
	if created2 {
		t.Fatal("expected created=false on existing disk")
	}
	got := make([]byte, len(sentinel))
	f2, err := os.Open(diskPath)
	if err != nil {
		t.Fatalf("reopen disk: %v", err)
	}
	_, _ = io.ReadFull(f2, got)
	_ = f2.Close()
	if string(got) != string(sentinel) {
		t.Fatalf("existing disk was modified: got %q, want %q", got, sentinel)
	}
}

func TestBuildToolsImageDeterministic(t *testing.T) {
	mkfs, err := exec.LookPath("mkfs.erofs")
	if err != nil {
		t.Skip("mkfs.erofs not installed")
	}
	dir := t.TempDir()
	fakeInit := filepath.Join(dir, "fake-init")
	if err := os.WriteFile(fakeInit, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("write fake init: %v", err)
	}

	out1 := filepath.Join(dir, "tools1.erofs")
	out2 := filepath.Join(dir, "tools2.erofs")
	rec1, err := BuildToolsImage(context.Background(), fakeInit, out1, ToolsBuildOptions{MkfsErofs: mkfs})
	if err != nil {
		t.Fatalf("BuildToolsImage 1: %v", err)
	}
	rec2, err := BuildToolsImage(context.Background(), fakeInit, out2, ToolsBuildOptions{MkfsErofs: mkfs})
	if err != nil {
		t.Fatalf("BuildToolsImage 2: %v", err)
	}
	if rec1.RootHash != rec2.RootHash || rec1.SHA256 != rec2.SHA256 || rec1.DataBlocks == 0 {
		t.Fatalf("tools image not deterministic: %+v vs %+v", rec1, rec2)
	}
	if len(rec1.RootHash) != 64 || rec1.Salt != ZeroSaltHex {
		t.Fatalf("unexpected tools verity record: %+v", rec1)
	}
}

// shortSockDir creates a short temporary directory under /tmp so Unix domain
// socket paths stay well below macOS's 104-byte sun_path limit.
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "chv-*")
	if err != nil {
		t.Fatalf("create short socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDialVsockHandshakeAndRetry(t *testing.T) {
	dir := shortSockDir(t)
	sock := filepath.Join(dir, "vm.vsock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	// Reject first connection (simulating guest init not listening yet), accept second.
	attempts := 0
	srvErr := make(chan error, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			attempts++
			buf := make([]byte, 64)
			n, rerr := c.Read(buf)
			if rerr != nil {
				_ = c.Close()
				continue
			}
			if string(buf[:n]) != "CONNECT 1024\n" {
				srvErr <- errors.New("unexpected CONNECT: " + string(buf[:n]))
				_ = c.Close()
				return
			}
			if attempts == 1 {
				_ = c.Close()
				continue
			}
			_, _ = io.WriteString(c, "OK 1073741824\nHELLO")
			_ = c.Close()
			srvErr <- nil
			return
		}
	}()

	vmmDone := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, reply, err := DialVsockRetry(ctx, sock, DefaultVsockPort, 10*time.Millisecond, vmmDone)
	if err != nil {
		t.Fatalf("DialVsockRetry: %v", err)
	}
	defer conn.Close()
	if reply != "OK 1073741824" {
		t.Errorf("reply = %q, want OK 1073741824", reply)
	}
	payload, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read payload after handshake: %v", err)
	}
	if string(payload) != "HELLO" {
		t.Errorf("payload = %q, want HELLO", payload)
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("server error: %v", err)
	}
	// Spec 20.3: supervisor must NEVER create a <sock>_<port> listener.
	if _, err := os.Stat(sock + "_1024"); !os.IsNotExist(err) {
		t.Fatalf("unexpected guest-to-host listener %s_1024", sock)
	}
}

func TestProbeWithScenarios(t *testing.T) {
	ctx := context.Background()
	fakeReg := fakeFileInfo{name: "f"}
	rp := RuntimePaths{
		Dir:      "/opt/runtime",
		VMM:      "/opt/runtime/cloud-hypervisor",
		CHRemote: "/opt/runtime/ch-remote",
		Kernel:   "/opt/runtime/vmlinux",
	}
	okDeps := func() ProbeDeps {
		return ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "amd64",
			KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
			Stat: func(p string) (os.FileInfo, error) {
				switch p {
				case devKVMPath, "/sbin/mkfs.ext4", rp.VMM, rp.CHRemote, rp.Kernel:
					return fakeReg, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			CheckKVMRW:   func(string) error { return nil },
			LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
			RuntimePaths: func() RuntimePaths { return rp },
			CurrentUser:  func() string { return "alice" },
			RunCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte("cloud-hypervisor v55.0\n"), nil
			},
		}
	}

	t.Run("all ready", func(t *testing.T) {
		p := ProbeWith(ctx, okDeps())
		if !p.OK || p.Driver != DriverName || p.Level != wall.MicroVM || p.Version != "55.0" {
			t.Fatalf("probe = %+v", p)
		}
	})
	t.Run("non-linux", func(t *testing.T) {
		d := okDeps()
		d.GOOS = "darwin"
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "Linux") {
			t.Fatalf("probe = %+v", p)
		}
	})
	t.Run("old kernel", func(t *testing.T) {
		d := okDeps()
		d.KernelRelease = func() string { return "6.1.0-generic" }
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "6.8") {
			t.Fatalf("probe = %+v", p)
		}
	})
	t.Run("kvm permission denied uses single sudo hint", func(t *testing.T) {
		d := okDeps()
		d.CheckKVMRW = func(string) error { return os.ErrPermission }
		d.LookPath = func(name string) (string, error) {
			if name == "setfacl" {
				return "/usr/bin/setfacl", nil
			}
			return "", os.ErrNotExist
		}
		p := ProbeWith(ctx, d)
		want := `sudo sh -c 'setfacl -m "u:$1:rw" /dev/kvm && usermod -aG kvm "$1"' _ alice`
		if p.OK || strings.Count(p.Fix, "sudo") != 1 || p.Fix != want {
			t.Fatalf("probe.Fix = %q, want %q (probe=%+v)", p.Fix, want, p)
		}
	})
	t.Run("kvm permission denied with empty user keeps expandable $USER outside single quotes", func(t *testing.T) {
		d := okDeps()
		d.CurrentUser = func() string { return "" }
		d.CheckKVMRW = func(string) error { return os.ErrPermission }
		d.LookPath = func(name string) (string, error) {
			if name == "setfacl" {
				return "/usr/bin/setfacl", nil
			}
			return "", os.ErrNotExist
		}
		p := ProbeWith(ctx, d)
		want := `sudo sh -c 'setfacl -m "u:$1:rw" /dev/kvm && usermod -aG kvm "$1"' _ "$USER"`
		if p.OK || p.Fix != want {
			t.Fatalf("probe.Fix = %q, want %q", p.Fix, want)
		}

		d.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
		p2 := ProbeWith(ctx, d)
		if p2.OK || p2.Fix != `sudo usermod -aG kvm "$USER"` {
			t.Fatalf("probe2.Fix = %q, want sudo usermod -aG kvm \"$USER\"", p2.Fix)
		}
	})
	t.Run("kvm permission denied shell-quotes metacharacters in username", func(t *testing.T) {
		d := okDeps()
		d.CurrentUser = func() string { return "me;id$(whoami)'x" }
		d.CheckKVMRW = func(string) error { return os.ErrPermission }
		d.LookPath = func(name string) (string, error) {
			if name == "setfacl" {
				return "/usr/bin/setfacl", nil
			}
			return "", os.ErrNotExist
		}
		p := ProbeWith(ctx, d)
		want := `sudo sh -c 'setfacl -m "u:$1:rw" /dev/kvm && usermod -aG kvm "$1"' _ 'me;id$(whoami)'\''x'`
		if p.OK || p.Fix != want {
			t.Fatalf("probe.Fix = %q, want %q", p.Fix, want)
		}

		d.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
		p2 := ProbeWith(ctx, d)
		want2 := `sudo usermod -aG kvm 'me;id$(whoami)'\''x'`
		if p2.OK || p2.Fix != want2 {
			t.Fatalf("probe2.Fix = %q, want %q", p2.Fix, want2)
		}
	})
	t.Run("missing runtime binary", func(t *testing.T) {
		d := okDeps()
		baseStat := d.Stat
		d.Stat = func(p string) (os.FileInfo, error) {
			if p == rp.VMM {
				return nil, os.ErrNotExist
			}
			return baseStat(p)
		}
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "cloud-hypervisor") {
			t.Fatalf("probe = %+v", p)
		}
	})
}

type fakeFileInfo struct{ name string }

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 1 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
