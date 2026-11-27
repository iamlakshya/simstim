package jail

import (
	"encoding/binary"
	"runtime"
	"slices"
	"testing"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
)

// call is one system call, as a seccomp filter sees it.
type call struct {
	name string
	arch uint32 // 0: this machine's
	nr   uint32 // used when name is empty
	args [6]uint64
}

// seccompData lays out struct seccomp_data for the bpf package's VM. The VM
// loads words in network order, and the kernel in the machine's, so each
// 32-bit word goes in big-endian at the offset where the kernel has it on a
// little-endian machine, as both of the jail's architectures are.
func seccompData(nr, archID uint32, args [6]uint64) []byte {
	b := make([]byte, 64)
	binary.BigEndian.PutUint32(b[0:], nr)
	binary.BigEndian.PutUint32(b[4:], archID)
	for i, a := range args {
		off := 16 + 8*i
		binary.BigEndian.PutUint32(b[off:], uint32(a))       // low word first
		binary.BigEndian.PutUint32(b[off+4:], uint32(a>>32)) // then the high word
	}
	return b
}

// verdict runs a jail's filters on a call and returns what the kernel would
// do: the strictest of their answers.
func verdict(t *testing.T, progs [][]bpf.RawInstruction, c call) uint32 {
	t.Helper()
	info, err := arch.GetInfo("")
	if err != nil {
		t.Skipf("no seccomp tables for %s: %v", runtime.GOARCH, err)
	}
	nr, archID := c.nr, uint32(info.ID)
	if c.name != "" {
		n, ok := info.SyscallNames[c.name]
		if !ok {
			t.Fatalf("no system call %q on %s", c.name, info.Name)
		}
		nr = uint32(n)
	}
	if c.arch != 0 {
		archID = c.arch
	}
	data := seccompData(nr, archID, c.args)
	strictest := uint32(retAllow)
	for _, raw := range progs {
		insts, ok := bpf.Disassemble(raw)
		if !ok {
			t.Fatal("a filter does not disassemble")
		}
		vm, err := bpf.NewVM(insts)
		if err != nil {
			t.Fatal(err)
		}
		// The VM returns the filter's value as a length: it is the action.
		out, err := vm.Run(data)
		if err != nil {
			t.Fatal(err)
		}
		if a := uint32(out); rank(a) > rank(strictest) {
			strictest = a
		}
	}
	return strictest
}

// rank orders seccomp actions as the kernel does: a kill beats an errno,
// which beats an allow.
func rank(a uint32) int {
	switch a & 0xffff0000 {
	case retKillProcess:
		return 3
	case retErrno:
		return 2
	default:
		return 1
	}
}

const (
	eperm  = retErrno | 1
	enosys = retErrno | errnoENOSYS
)

func TestProfilesRefuseWhatNoJailNeeds(t *testing.T) {
	for _, p := range []Profile{ProfileVMM, ProfileConverter, ProfileGit, ProfileEgress} {
		for _, n := range []NetMode{NetNone, NetHost} {
			progs, err := filters(p, n, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range refused {
				if got := verdict(t, progs, call{name: name}); got != eperm {
					t.Errorf("%s/%d: %s = %#x, want EPERM", p, n, name, got)
				}
			}
			checks := []struct {
				c    call
				want uint32
			}{
				{call{name: "read"}, retAllow},
				{call{name: "execve"}, retAllow},
				{call{name: "clone3"}, enosys},
				{call{name: "clone", args: [6]uint64{0x10000000}}, eperm}, // CLONE_NEWUSER
				{call{name: "clone", args: [6]uint64{0x40000000}}, eperm}, // CLONE_NEWNET
				{call{name: "clone", args: [6]uint64{0x003d0f00}}, retAllow},
				{call{name: "socket", args: [6]uint64{afUnix}}, retAllow},
				{call{name: "socket", args: [6]uint64{40}}, eperm},              // AF_VSOCK
				{call{name: "socket", args: [6]uint64{17}}, eperm},              // AF_PACKET
				{call{name: "socket", args: [6]uint64{afNetlink, 3, 9}}, eperm}, // NETLINK_AUDIT
				// Stray high bits make a family that the kernel would read as
				// AF_INET. The filter refuses it rather than allow it.
				{call{name: "socket", args: [6]uint64{1<<32 | afInet}}, eperm},
				{call{name: "socket", args: [6]uint64{1<<32 | afUnix}}, eperm},
				// The architecture guard kills a call of another architecture.
				{call{nr: 0, arch: 0x40000003}, retKillProcess}, // i386
				{call{nr: 0, arch: 0x40000028}, retKillProcess}, // 32-bit Arm
			}
			for _, c := range checks {
				if got := verdict(t, progs, c.c); got != c.want {
					t.Errorf("%s/%d: %+v = %#x, want %#x", p, n, c.c, got, c.want)
				}
			}
		}
	}
}

func TestOnlyEgressReachesTheNetwork(t *testing.T) {
	inet := call{name: "socket", args: [6]uint64{afInet, 1, 6}}
	inet6 := call{name: "socket", args: [6]uint64{afInet6, 1, 6}}
	route := call{name: "socket", args: [6]uint64{afNetlink, 3, netlinkRoute}}
	cases := []struct {
		p                        Profile
		n                        NetMode
		inet, inet6, routeAnswer uint32
	}{
		{ProfileEgress, NetHost, retAllow, retAllow, retAllow},
		{ProfileEgress, NetNone, eperm, eperm, retAllow},
		{ProfileVMM, NetHost, eperm, eperm, eperm},
		{ProfileVMM, NetNone, eperm, eperm, eperm},
		{ProfileGit, NetNone, eperm, eperm, eperm},
		{ProfileConverter, NetNone, eperm, eperm, eperm},
	}
	for _, c := range cases {
		progs, err := filters(c.p, c.n, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range []struct {
			c    call
			want uint32
		}{{inet, c.inet}, {inet6, c.inet6}, {route, c.routeAnswer}} {
			if got := verdict(t, progs, x.c); got != x.want {
				t.Errorf("%s/%d: socket%v = %#x, want %#x", c.p, c.n, x.c.args[:3], got, x.want)
			}
		}
	}
}

// TestNoProfileTypesIntoATerminal checks the refusal of TIOCSTI and
// TIOCLINUX, with high bits on the request too, which the kernel drops.
func TestNoProfileTypesIntoATerminal(t *testing.T) {
	for _, p := range []Profile{ProfileVMM, ProfileConverter, ProfileGit, ProfileEgress} {
		for _, landlockMode := range []bool{false, true} {
			progs, err := filters(p, NetHost, landlockMode)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct {
				req  uint64
				want uint32
			}{
				{tiocsti, eperm}, {tioclinux, eperm}, {1<<32 | tiocsti, eperm}, {0xffffffff00000000 | tioclinux, eperm},
				{0x5401, retAllow}, // TCGETS
				{0x541b, retAllow}, // FIONREAD
			} {
				if got := verdict(t, progs, call{name: "ioctl", args: [6]uint64{0, c.req}}); got != c.want {
					t.Errorf("%s (landlock mode %v): ioctl %#x = %#x, want %#x", p, landlockMode, c.req, got, c.want)
				}
			}
		}
	}
}

// TestLandlockModeKeepsTheProcessGroup checks that without a user namespace
// no process of a jail can leave its session or process group.
func TestLandlockModeKeepsTheProcessGroup(t *testing.T) {
	for _, landlockMode := range []bool{false, true} {
		progs, err := filters(ProfileGit, NetNone, landlockMode)
		if err != nil {
			t.Fatal(err)
		}
		want := uint32(retAllow)
		if landlockMode {
			want = eperm
		}
		for _, name := range []string{"setsid", "setpgid"} {
			if got := verdict(t, progs, call{name: name}); got != want {
				t.Errorf("landlock mode %v: %s = %#x, want %#x", landlockMode, name, got, want)
			}
		}
	}
}

// TestLandlockModeRefusesMetadataByPath checks that without a user
// namespace, a jail cannot change a file's mode, owner, times or xattrs by
// its path, which Landlock does not check, but still can on a descriptor.
func TestLandlockModeRefusesMetadataByPath(t *testing.T) {
	info, err := arch.GetInfo("")
	if err != nil {
		t.Skip(err)
	}
	for _, landlockMode := range []bool{false, true} {
		progs, err := filters(ProfileGit, NetNone, landlockMode)
		if err != nil {
			t.Fatal(err)
		}
		byPath := uint32(retAllow)
		if landlockMode {
			byPath = eperm
		}
		var calls []call
		for _, n := range append(slices.Clone(pathMeta), legacyPathMeta...) {
			if _, ok := info.SyscallNames[n]; ok {
				calls = append(calls, call{name: n, args: [6]uint64{0, 0x7f0000001000}})
			}
		}
		for _, c := range calls {
			if got := verdict(t, progs, c); got != byPath {
				t.Errorf("landlock mode %v: %s = %#x, want %#x", landlockMode, c.name, got, byPath)
			}
		}
		for _, c := range []struct {
			c    call
			want uint32
		}{
			{call{name: "utimensat", args: [6]uint64{3, 0x7f0000001000}}, byPath},
			{call{name: "utimensat", args: [6]uint64{3, 1 << 32}}, byPath},
			{call{name: "utimensat", args: [6]uint64{3, 0}}, retAllow}, // on the descriptor itself
			{call{name: "fchmod"}, retAllow},
			{call{name: "fchown"}, retAllow},
			{call{name: "fsetxattr"}, retAllow},
			{call{name: "fremovexattr"}, retAllow},
		} {
			if got := verdict(t, progs, c.c); got != c.want {
				t.Errorf("landlock mode %v: %s%v = %#x, want %#x", landlockMode, c.c.name, c.c.args[:2], got, c.want)
			}
		}
	}
}

func TestEveryRefusedCallExistsOnBothArchitectures(t *testing.T) {
	for _, n := range legacyPathMeta {
		if _, ok := arch.X86_64.SyscallNames[n]; !ok {
			t.Errorf("x86_64 has no system call %q", n)
		}
	}
	names := append([]string{"clone", "clone3", "socket", "ioctl", "setsid", "setpgid", "utimensat",
		"prlimit64", "sched_setscheduler", "sched_setattr", "sched_setparam", "sched_setaffinity", "setpriority", "ioprio_set"}, refused...)
	names = append(names, pathMeta...)
	for _, info := range []*arch.Info{arch.X86_64, arch.AARCH64} {
		for _, n := range names {
			if _, ok := info.SyscallNames[n]; !ok {
				t.Errorf("%s has no system call %q", info.Name, n)
			}
		}
	}
}

func TestGuardKnowsOnlyTheHelpersArchitectures(t *testing.T) {
	for goarch, want := range map[string]uint32{"amd64": auditArchX86_64, "arm64": auditArchAARCH64} {
		insts, err := guard(goarch)
		if err != nil {
			t.Fatal(err)
		}
		if j, ok := insts[1].(bpf.JumpIf); !ok || j.Val != want {
			t.Errorf("guard(%s) compares with %+v, want %#x", goarch, insts[1], want)
		}
		if uint32(arch.X86_64.ID) != auditArchX86_64 || uint32(arch.AARCH64.ID) != auditArchAARCH64 {
			t.Error("the audit architectures differ from go-seccomp-bpf's")
		}
	}
	if _, err := guard("386"); err == nil || hintOf(err) == "" {
		t.Errorf("guard(386) = %v, want a refusal with a hint", err)
	}
}
