//go:build linux

package jail

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const testProbeSubcmd = "jail-test-probe"

func TestMain(m *testing.M) {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case ExecSubcommand:
			if err := RunExec(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case testProbeSubcmd:
			runTestProbe(os.Args[2:])
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

type probeReport struct {
	HomeBlocked     bool   `json:"home_blocked"`
	HomeErr         string `json:"home_err,omitempty"`
	SecretBlocked   bool   `json:"secret_blocked"`
	SecretErr       string `json:"secret_err,omitempty"`
	TCPBlocked      bool   `json:"tcp_blocked"`
	TCPErr          string `json:"tcp_err,omitempty"`
	PtraceBlocked   bool   `json:"ptrace_blocked"`
	PtraceErrno     int    `json:"ptrace_errno,omitempty"`
	ProcMemBlocked  bool   `json:"proc_mem_blocked"`
	ProcMemErr      string `json:"proc_mem_err,omitempty"`
	UnshareBlocked  bool   `json:"unshare_blocked"`
	UnshareErrno    int    `json:"unshare_errno,omitempty"`
	Clone3Errno     int    `json:"clone3_errno,omitempty"`
	ReadAllowed     string `json:"read_allowed,omitempty"`
	TruncateBlocked bool   `json:"truncate_blocked,omitempty"`
	WriteAllowedOK  bool   `json:"write_allowed_ok,omitempty"`
	FD3Data         string `json:"fd3_data,omitempty"`
	NoNewPrivs      string `json:"no_new_privs,omitempty"`
	CapEff          string `json:"cap_eff,omitempty"`
	StatusGroups    int    `json:"status_groups,omitempty"`
	GetGroups       []int  `json:"get_groups,omitempty"`
	OpenExtraOK     bool   `json:"open_extra_ok,omitempty"`
	OpenExtraErr    string `json:"open_extra_err,omitempty"`
	KVMIoctlAPI     int    `json:"kvm_ioctl_api,omitempty"`
}

func runTestProbe(args []string) {
	fs := flag.NewFlagSet(testProbeSubcmd, flag.ExitOnError)
	homePath := fs.String("home", "", "")
	secretPath := fs.String("secret", "", "")
	tcpAddr := fs.String("tcp", "", "")
	parentPID := fs.Int("parent-pid", 0, "")
	readFile := fs.String("read-file", "", "")
	writeFile := fs.String("write-file", "", "")
	readFD3 := fs.Bool("fd3", false, "")
	openExtra := fs.String("open-extra", "", "")
	kvmDev := fs.String("kvm", "", "")
	_ = fs.Parse(args)

	var rep probeReport

	if *homePath != "" {
		f, err := os.Open(*homePath)
		if err != nil {
			rep.HomeBlocked = true
			rep.HomeErr = err.Error()
		} else {
			_ = f.Close()
		}
	}

	if *secretPath != "" {
		if _, err := os.ReadFile(*secretPath); err != nil {
			rep.SecretBlocked = true
			rep.SecretErr = err.Error()
		}
	}

	if *tcpAddr != "" {
		conn, err := net.DialTimeout("tcp", *tcpAddr, 2*time.Second)
		if err != nil {
			rep.TCPBlocked = true
			rep.TCPErr = err.Error()
		} else {
			_ = conn.Close()
		}
	}

	if *parentPID > 0 {
		_, _, errno := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_ATTACH, uintptr(*parentPID), 0, 0, 0, 0)
		if errno != 0 {
			rep.PtraceBlocked = true
			rep.PtraceErrno = int(errno)
		} else {
			_, _, _ = unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_DETACH, uintptr(*parentPID), 0, 0, 0, 0)
		}

		f, err := os.Open(fmt.Sprintf("/proc/%d/mem", *parentPID))
		if err != nil {
			rep.ProcMemBlocked = true
			rep.ProcMemErr = err.Error()
		} else {
			_ = f.Close()
		}
	}

	if err := unix.Unshare(unix.CLONE_NEWUSER); err != nil {
		rep.UnshareBlocked = true
		var errno syscall.Errno
		if errors.As(err, &errno) {
			rep.UnshareErrno = int(errno)
		}
	}

	_, _, c3 := unix.Syscall(unix.SYS_CLONE3, 0, 0, 0)
	rep.Clone3Errno = int(c3)

	if *readFile != "" {
		if b, err := os.ReadFile(*readFile); err == nil {
			rep.ReadAllowed = strings.TrimSpace(string(b))
		}
		if err := os.Truncate(*readFile, 0); err != nil {
			rep.TruncateBlocked = true
		}
	}

	if *writeFile != "" {
		if err := os.WriteFile(*writeFile, []byte("ok\n"), 0o600); err == nil {
			rep.WriteAllowedOK = true
		}
	}

	if *readFD3 {
		f := os.NewFile(3, "fd3")
		if f != nil {
			b, _ := io.ReadAll(io.LimitReader(f, 4096))
			_ = f.Close()
			rep.FD3Data = strings.TrimSpace(string(b))
		}
	}

	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			switch {
			case strings.HasPrefix(line, "NoNewPrivs:"):
				rep.NoNewPrivs = strings.TrimSpace(strings.TrimPrefix(line, "NoNewPrivs:"))
			case strings.HasPrefix(line, "CapEff:"):
				rep.CapEff = strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			case strings.HasPrefix(line, "Groups:"):
				rep.StatusGroups = len(strings.Fields(strings.TrimPrefix(line, "Groups:")))
			}
		}
	}
	if gs, err := os.Getgroups(); err == nil {
		rep.GetGroups = gs
	}

	if *openExtra != "" {
		f, err := os.Open(*openExtra)
		if err != nil {
			rep.OpenExtraErr = err.Error()
		} else {
			var buf [1]byte
			_, rerr := f.Read(buf[:])
			_ = f.Close()
			if rerr == nil || errors.Is(rerr, io.EOF) {
				rep.OpenExtraOK = true
			} else {
				rep.OpenExtraErr = rerr.Error()
			}
		}
	}

	if *kvmDev != "" {
		fd, err := unix.Open(*kvmDev, unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err == nil {
			// KVM_GET_API_VERSION = 0xae00
			v, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xae00, 0)
			_ = unix.Close(fd)
			if errno == 0 {
				rep.KVMIoctlAPI = int(v)
			}
		}
	}

	_ = json.NewEncoder(os.Stdout).Encode(rep)
}

func runProbeCmd(t *testing.T, cmd *exec.Cmd, userFileCount int) probeReport {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Start()
	CloseInternalFiles(cmd, userFileCount)
	if err != nil {
		t.Fatalf("start jailed command: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("jailed command failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	var rep probeReport
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &rep); err != nil {
		t.Fatalf("decode probe report: %v\nraw stdout: %q\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return rep
}

func startLocalTCPServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().String()
}

func TestSeccompBPFAssembles(t *testing.T) {
	for _, prof := range []Profile{"", ProfileVMM, ProfileConverter, ProfileGit, ProfileEgress} {
		for _, nm := range []NetMode{NetNone, NetHost} {
			raw, err := SeccompBPF(prof, nm)
			if err != nil {
				t.Fatalf("SeccompBPF(%q, %d): %v", prof, nm, err)
			}
			if len(raw) == 0 || len(raw)%8 != 0 {
				t.Fatalf("SeccompBPF(%q, %d) len=%d, want non-zero multiple of 8", prof, nm, len(raw))
			}
			// Instruction 0 must load seccomp_data.arch (offset 4) for host-ABI guard.
			firstOp := binary.NativeEndian.Uint16(raw[0:2])
			firstK := binary.NativeEndian.Uint32(raw[4:8])
			if firstOp != 0x20 || firstK != 4 {
				t.Fatalf("first BPF inst op=0x%x k=%d, want 0x20/4 (LD arch)", firstOp, firstK)
			}
		}
	}
}

func makeSeccompData(nr, auditArch uint32, args ...uint64) []byte {
	// golang.org/x/net/bpf.VM evaluates LoadAbsolute in network byte order
	// (big-endian per 32-bit word), whereas Linux seccomp accesses 32-bit words
	// of struct seccomp_data in native endianness (with arg low word at +0 and
	// high word at +4 on little-endian hosts).
	buf := make([]byte, 64)
	binary.BigEndian.PutUint32(buf[0:4], nr)
	binary.BigEndian.PutUint32(buf[4:8], auditArch)
	for i := 0; i < len(args) && i < 6; i++ {
		off := 16 + i*8
		lo := uint32(args[i])
		hi := uint32(args[i] >> 32)
		binary.BigEndian.PutUint32(buf[off:off+4], lo)
		binary.BigEndian.PutUint32(buf[off+4:off+8], hi)
	}
	return buf
}

func runSeccompVM(t *testing.T, raw []bpf.RawInstruction, data []byte) uint32 {
	t.Helper()
	var insts []bpf.Instruction
	for _, r := range raw {
		insts = append(insts, r.Disassemble())
	}
	vm, err := bpf.NewVM(insts)
	if err != nil {
		t.Fatalf("bpf.NewVM: %v", err)
	}
	res, err := vm.Run(data)
	if err != nil {
		t.Fatalf("vm.Run: %v", err)
	}
	return uint32(res)
}

func TestSeccompBPFVMNoAccumulatorClobber(t *testing.T) {
	info, err := arch.GetInfo("")
	if err != nil {
		t.Fatalf("arch.GetInfo: %v", err)
	}
	hostArch := uint32(info.ID)
	wantAllow := uint32(seccomp.ActionAllow)
	wantEPERM := uint32(actionEPERM)
	wantENOSYS := uint32(actionENOSYS)
	wantKill := uint32(seccomp.ActionKillProcess)

	noneRaw, err := SeccompInstructions(ProfileVMM, NetNone)
	if err != nil {
		t.Fatalf("SeccompInstructions(NetNone): %v", err)
	}
	hostRaw, err := SeccompInstructions(ProfileEgress, NetHost)
	if err != nil {
		t.Fatalf("SeccompInstructions(NetHost): %v", err)
	}

	// Pick a foreign audit arch distinct from hostArch.
	foreignArch := uint32(unix.AUDIT_ARCH_I386)
	if hostArch == foreignArch {
		foreignArch = uint32(unix.AUDIT_ARCH_AARCH64)
	}

	cases := []struct {
		name     string
		raw      []bpf.RawInstruction
		data     []byte
		wantCode uint32
	}{
		{
			name:     "foreign ABI killed",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_GETPID, foreignArch),
			wantCode: wantKill,
		},
		{
			name:     "clone with CLONE_NEWUSER refused",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_CLONE, hostArch, unix.CLONE_NEWUSER),
			wantCode: wantEPERM,
		},
		{
			name:     "clone with flags == 41 (amd64 SYS_socket) allowed under NetNone",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_CLONE, hostArch, 41),
			wantCode: wantAllow,
		},
		{
			name:     "clone with flags == 198 (arm64 SYS_socket) allowed under NetNone",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_CLONE, hostArch, 198),
			wantCode: wantAllow,
		},
		{
			name:     "clone with flags == SYS_SOCKET allowed under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_CLONE, hostArch, unix.SYS_SOCKET),
			wantCode: wantAllow,
		},
		{
			name:     "clone3 returns ENOSYS",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_CLONE3, hostArch, 0, 0),
			wantCode: wantENOSYS,
		},
		{
			name:     "ptrace refused",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_PTRACE, hostArch, unix.PTRACE_ATTACH, 1),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_UNIX allowed under NetNone",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_UNIX, unix.SOCK_STREAM, 0),
			wantCode: wantAllow,
		},
		{
			name:     "socket AF_INET refused under NetNone",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_INET, unix.SOCK_STREAM, 0),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_VSOCK refused under NetNone",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_VSOCK, unix.SOCK_STREAM, 0),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_VSOCK refused under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_VSOCK, unix.SOCK_STREAM, 0),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_NETLINK NETLINK_AUDIT refused under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_AUDIT),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_NETLINK NETLINK_ROUTE allowed under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE),
			wantCode: wantAllow,
		},
		{
			name:     "unshare CLONE_NEWTIME refused",
			raw:      noneRaw,
			data:     makeSeccompData(unix.SYS_UNSHARE, hostArch, unix.CLONE_NEWTIME),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_INET6 allowed under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_INET6, unix.SOCK_STREAM, 0),
			wantCode: wantAllow,
		},
		{
			name:     "socket AF_PACKET refused under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_PACKET, unix.SOCK_RAW, 0),
			wantCode: wantEPERM,
		},
		{
			name:     "socket AF_INET allowed under NetHost",
			raw:      hostRaw,
			data:     makeSeccompData(unix.SYS_SOCKET, hostArch, unix.AF_INET, unix.SOCK_STREAM, 0),
			wantCode: wantAllow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runSeccompVM(t, tc.raw, tc.data); got != tc.wantCode {
				t.Errorf("BPF VM returned 0x%x, want 0x%x", got, tc.wantCode)
			}
		})
	}
}

func TestCommandRefusesLowLandlockABIWithoutUserNS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	for _, abi := range []int{0, 1, 2} {
		d := defaultDeps()
		d.userns = func(context.Context, string) bool { return false }
		d.landlockABI = func() int { return abi }
		d.selfExe = func() (string, error) { return selfExe, nil }

		cmd, err := commandWithDeps(ctx, Spec{
			Argv:    []string{selfExe, testProbeSubcmd},
			Net:     NetNone,
			Seccomp: ProfileGit,
		}, d)
		if err == nil {
			CloseInternalFiles(cmd, 0)
			t.Fatalf("ABI %d without userns: commandWithDeps succeeded, want refusal", abi)
		}
		if !strings.Contains(err.Error(), "Landlock ABI 3") || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("ABI %d error = %q, want ABI 3 refusal with Try:", abi, err.Error())
		}
	}

	// ABI 3 without userns must succeed in building the jail-exec command.
	d := defaultDeps()
	d.userns = func(context.Context, string) bool { return false }
	d.landlockABI = func() int { return 3 }
	d.selfExe = func() (string, error) { return selfExe, nil }
	cmd, err := commandWithDeps(ctx, Spec{
		Argv:    []string{selfExe, testProbeSubcmd},
		Net:     NetNone,
		Seccomp: ProfileGit,
	}, d)
	if err != nil {
		t.Fatalf("ABI 3 without userns failed: %v", err)
	}
	defer CloseInternalFiles(cmd, 0)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		t.Fatalf("command must not set Credential/setgroups: %+v", cmd.SysProcAttr.Credential)
	}
}

func TestUsernsMatchesLiveTmpfsProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := Userns()
	want := probeUserNS(ctx, "")
	if got != want {
		t.Fatalf("Userns() = %v, fresh tmpfs-in-userns probe = %v", got, want)
	}
}

func TestJailLimitsBothModes(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if f, err := os.Open(home); err != nil {
		t.Skipf("parent cannot open $HOME (%s): %v", home, err)
	} else {
		_ = f.Close()
	}

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	tcpAddr := startLocalTCPServer(t)

	tmp := t.TempDir()
	secretFile := filepath.Join(tmp, "unbound-secret.txt")
	roFile := filepath.Join(tmp, "allowed-ro.txt")
	rwDir := filepath.Join(tmp, "allowed-rw")
	if err := os.WriteFile(secretFile, []byte("TOP-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(roFile, []byte("HELLO-RO\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rwDir, 0o700); err != nil {
		t.Fatal(err)
	}

	modes := []struct {
		name      string
		wantNS    bool
		withRules bool
	}{
		{name: "with_userns_bwrap", wantNS: true},
		{name: "with_userns_bwrap_and_landlock", wantNS: true, withRules: true},
		{name: "without_userns_landlock_seccomp", wantNS: false},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			d := defaultDeps()
			d.selfExe = func() (string, error) { return selfExe, nil }

			if mode.wantNS {
				if !probeUserNS(ctx, "") {
					t.Skip("user namespaces with tmpfs mount are unavailable on this host")
				}
				if mode.withRules && landlockKernelABI() < 1 {
					t.Skip("Landlock unavailable on this kernel")
				}
			} else {
				if abi := landlockKernelABI(); abi < MinNoUserNSLandlockABI {
					t.Skipf("kernel Landlock ABI %d is below %d", abi, MinNoUserNSLandlockABI)
				}
				d.userns = func(context.Context, string) bool { return false }
			}

			rPipe, wPipe, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			defer rPipe.Close()
			if _, err := io.WriteString(wPipe, "FD3-PAYLOAD\n"); err != nil {
				_ = wPipe.Close()
				t.Fatalf("write pipe: %v", err)
			}
			_ = wPipe.Close()

			outFile := filepath.Join(rwDir, mode.name+".out")
			spec := Spec{
				Argv: []string{
					selfExe, testProbeSubcmd,
					"--home", home,
					"--secret", secretFile,
					"--tcp", tcpAddr,
					"--parent-pid", strconv.Itoa(os.Getpid()),
					"--read-file", roFile,
					"--write-file", outFile,
					"--fd3",
				},
				Net: NetNone,
				Binds: []Bind{
					{Src: roFile, Mode: BindRO},
					{Src: rwDir, Mode: BindRW},
				},
				Seccomp:    ProfileConverter,
				Files:      []*os.File{rPipe},
				ExecHelper: selfExe,
			}
			if mode.withRules {
				spec.Rules = []PathRule{
					{Path: roFile, Write: false},
					{Path: rwDir, Write: true},
				}
			}

			cmd, err := commandWithDeps(ctx, spec, d)
			if err != nil {
				t.Fatalf("commandWithDeps: %v", err)
			}
			rep := runProbeCmd(t, cmd, len(spec.Files))

			if !rep.HomeBlocked {
				t.Errorf("jailed command opened $HOME (%s) unexpectedly", home)
			}
			if !rep.SecretBlocked {
				t.Errorf("jailed command read unbound secret file %s unexpectedly", secretFile)
			}
			if !rep.TCPBlocked {
				t.Errorf("jailed command connected to TCP %s despite NetNone", tcpAddr)
			}
			if !rep.PtraceBlocked {
				t.Errorf("jailed command attached ptrace to parent PID %d unexpectedly", os.Getpid())
			}
			if !rep.ProcMemBlocked {
				t.Errorf("jailed command opened /proc/%d/mem unexpectedly", os.Getpid())
			}
			if !rep.UnshareBlocked {
				t.Errorf("jailed command called unshare(CLONE_NEWUSER) unexpectedly")
			}
			if rep.Clone3Errno != int(unix.ENOSYS) {
				t.Errorf("clone3 errno = %d, want ENOSYS (%d)", rep.Clone3Errno, unix.ENOSYS)
			}
			if rep.NoNewPrivs != "1" {
				t.Errorf("NoNewPrivs = %q, want 1", rep.NoNewPrivs)
			}
			if rep.CapEff != "0000000000000000" {
				t.Errorf("CapEff = %q, want 0000000000000000", rep.CapEff)
			}
			if rep.ReadAllowed != "HELLO-RO" {
				t.Errorf("ReadAllowed = %q, want HELLO-RO", rep.ReadAllowed)
			}
			if !rep.TruncateBlocked {
				t.Errorf("jailed command truncated read-only file %s unexpectedly", roFile)
			}
			if !rep.WriteAllowedOK {
				t.Errorf("WriteAllowedOK = false, want true for bound RW dir %s", rwDir)
			}
			if rep.FD3Data != "FD3-PAYLOAD" {
				t.Errorf("FD3Data = %q, want FD3-PAYLOAD", rep.FD3Data)
			}
		})
	}
}

func TestJailNetHostAllowsTCPBothModes(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("UserHomeDir: %v", err)
	}
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	tcpAddr := startLocalTCPServer(t)

	for _, wantNS := range []bool{true, false} {
		name := "without_userns"
		if wantNS {
			name = "with_userns"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			d := defaultDeps()
			d.selfExe = func() (string, error) { return selfExe, nil }
			if wantNS {
				if !probeUserNS(ctx, "") {
					t.Skip("user namespaces with tmpfs mount are unavailable on this host")
				}
			} else {
				if abi := landlockKernelABI(); abi < MinNoUserNSLandlockABI {
					t.Skipf("kernel Landlock ABI %d is below %d", abi, MinNoUserNSLandlockABI)
				}
				d.userns = func(context.Context, string) bool { return false }
			}

			spec := EgressSpec([]string{
				selfExe, testProbeSubcmd,
				"--home", home,
				"--tcp", tcpAddr,
				"--parent-pid", strconv.Itoa(os.Getpid()),
			}, nil)
			spec.ExecHelper = selfExe

			cmd, err := commandWithDeps(ctx, spec, d)
			if err != nil {
				t.Fatalf("commandWithDeps: %v", err)
			}
			rep := runProbeCmd(t, cmd, 0)

			if rep.TCPBlocked {
				t.Errorf("NetHost egress jail unexpectedly blocked TCP to %s: %s", tcpAddr, rep.TCPErr)
			}
			if !rep.HomeBlocked {
				t.Errorf("NetHost egress jail opened $HOME unexpectedly")
			}
			if !rep.PtraceBlocked || !rep.ProcMemBlocked {
				t.Errorf("NetHost egress jail failed ptrace/proc-mem check: %+v", rep)
			}
		})
	}
}

// findGroupOnlyReadableFile returns a host file owned by root:<supp-gid> (where
// mode has group-read and no world-read) that the current process can open
// solely because of its supplementary group list.
func findGroupOnlyReadableFile(hostGroups []int, primaryGID int) string {
	supp := map[uint32]bool{}
	for _, g := range hostGroups {
		if g != primaryGID {
			supp[uint32(g)] = true
		}
	}
	if len(supp) == 0 {
		return ""
	}
	entries, err := os.ReadDir("/var/log")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join("/var/log", e.Name())
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		// Require owner != us, world-read == 0, group-read != 0, and GID in our supplementary groups.
		if int(st.Uid) == os.Getuid() || st.Mode&0o004 != 0 || st.Mode&0o040 == 0 || !supp[st.Gid] {
			continue
		}
		if f, err := os.Open(p); err == nil {
			_ = f.Close()
			return p
		}
	}
	return ""
}

func TestVMMJailKeepsGroupsNeverCallsSetgroups(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	hostGroups, err := os.Getgroups()
	if err != nil {
		t.Fatalf("os.Getgroups: %v", err)
	}
	groupFile := findGroupOnlyReadableFile(hostGroups, os.Getgid())

	kvmAccessible := false
	if fd, err := unix.Open("/dev/kvm", unix.O_RDWR|unix.O_CLOEXEC, 0); err == nil {
		_ = unix.Close(fd)
		kvmAccessible = true
	}

	for _, wantNS := range []bool{true, false} {
		name := "without_userns"
		if wantNS {
			name = "with_userns"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			d := defaultDeps()
			d.selfExe = func() (string, error) { return selfExe, nil }
			if wantNS {
				if !probeUserNS(ctx, "") {
					t.Skip("user namespaces with tmpfs mount are unavailable on this host")
				}
			} else {
				if abi := landlockKernelABI(); abi < MinNoUserNSLandlockABI {
					t.Skipf("kernel Landlock ABI %d is below %d", abi, MinNoUserNSLandlockABI)
				}
				d.userns = func(context.Context, string) bool { return false }
			}

			argv := []string{selfExe, testProbeSubcmd}
			var roPaths []string
			if groupFile != "" {
				roPaths = append(roPaths, groupFile)
				argv = append(argv, "--open-extra", groupFile)
			}
			if kvmAccessible {
				argv = append(argv, "--kvm", "/dev/kvm")
			}

			spec := VMMSpec(argv, roPaths, nil)
			// Only bind /dev/kvm in bwrap when /dev/kvm exists on this host.
			if _, statErr := os.Stat("/dev/kvm"); statErr != nil {
				spec.Binds = spec.Binds[1:]
			}
			spec.ExecHelper = selfExe

			cmd, err := commandWithDeps(ctx, spec, d)
			if err != nil {
				t.Fatalf("commandWithDeps: %v", err)
			}
			if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
				t.Fatalf("vmm jail must never set Credential/setgroups: %+v", cmd.SysProcAttr.Credential)
			}
			rep := runProbeCmd(t, cmd, 0)

			// Neither bwrap nor jail-exec calls setgroups(2), so every host
			// supplementary group remains present in the kernel cred.
			if rep.StatusGroups != len(hostGroups) {
				t.Errorf("vmm jail Groups count = %d, want %d (host groups: %v)", rep.StatusGroups, len(hostGroups), hostGroups)
			}
			if !wantNS {
				if len(rep.GetGroups) != len(hostGroups) {
					t.Errorf("no-userns vmm jail Getgroups = %v, want %v", rep.GetGroups, hostGroups)
				}
			}
			if groupFile != "" && !rep.OpenExtraOK {
				t.Errorf("vmm jail failed to open supplementary-group-only file %s: %s", groupFile, rep.OpenExtraErr)
			}
			if kvmAccessible && rep.KVMIoctlAPI != 12 {
				t.Errorf("vmm jail KVM_GET_API_VERSION = %d, want 12", rep.KVMIoctlAPI)
			}
		})
	}
}

func TestJailGitBundleBothModes(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	outDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, gitBin, append([]string{"-C", repo}, args...)...)
		c.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + tmp,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.invalid",
		}
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "hello.txt")
	runGit("commit", "-q", "-m", "initial")

	for _, wantNS := range []bool{true, false} {
		name := "without_userns"
		if wantNS {
			name = "with_userns"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			d := defaultDeps()
			d.selfExe = func() (string, error) { return selfExe, nil }
			if wantNS {
				if !probeUserNS(ctx, "") {
					t.Skip("user namespaces with tmpfs mount are unavailable on this host")
				}
			} else {
				if abi := landlockKernelABI(); abi < MinNoUserNSLandlockABI {
					t.Skipf("kernel Landlock ABI %d is below %d", abi, MinNoUserNSLandlockABI)
				}
				d.userns = func(context.Context, string) bool { return false }
			}

			bundlePath := filepath.Join(outDir, name+".bundle")
			spec := GitSpec(
				[]string{gitBin, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "safe.directory=*", "bundle", "create", bundlePath, "HEAD"},
				repo,
				[]string{repo},
				[]string{outDir},
			)
			spec.Env = []string{"GIT_CONFIG_NOSYSTEM=1"}
			spec.ExecHelper = selfExe

			cmd, err := commandWithDeps(ctx, spec, d)
			if err != nil {
				t.Fatalf("commandWithDeps: %v", err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			startErr := cmd.Start()
			CloseInternalFiles(cmd, 0)
			if startErr != nil {
				t.Fatalf("start jailed git: %v", startErr)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("jailed git bundle create failed: %v\nstderr: %s", err, stderr.String())
			}
			fi, err := os.Stat(bundlePath)
			if err != nil || fi.Size() == 0 {
				t.Fatalf("bundle missing or empty: err=%v fi=%v", err, fi)
			}
		})
	}
}
