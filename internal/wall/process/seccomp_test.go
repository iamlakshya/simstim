package process

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// runFilter is an independent interpreter of the classic BPF subset. Its
// input has the kernel's seccomp_data layout, including complete arguments.
func runFilter(program []Instruction, nr, audit uint32, args ...uint64) (uint32, error) {
	var data [64]byte
	binary.LittleEndian.PutUint32(data[:4], nr)
	binary.LittleEndian.PutUint32(data[4:8], audit)
	for i, arg := range args {
		if i >= 6 {
			return 0, fmt.Errorf("too many syscall arguments")
		}
		binary.LittleEndian.PutUint64(data[16+i*8:], arg)
	}
	var accumulator uint32
	for pc, steps := 0, 0; steps < len(program)*2; steps++ {
		if pc < 0 || pc >= len(program) {
			return 0, fmt.Errorf("filter left its program")
		}
		instruction := program[pc]
		pc++
		switch instruction.Code {
		case 0x20: // BPF_LD | BPF_W | BPF_ABS
			if instruction.K > uint32(len(data)-4) {
				return 0, fmt.Errorf("filter read outside seccomp_data")
			}
			accumulator = binary.LittleEndian.Uint32(data[instruction.K:])
		case 0x15, 0x45: // BPF_JMP | BPF_JEQ/JSET | BPF_K
			matched := accumulator == instruction.K
			if instruction.Code == 0x45 {
				matched = accumulator&instruction.K != 0
			}
			if matched {
				pc += int(instruction.JT)
			} else {
				pc += int(instruction.JF)
			}
		case 0x06: // BPF_RET | BPF_K
			return instruction.K, nil
		default:
			return 0, fmt.Errorf("unsupported BPF opcode %x", instruction.Code)
		}
	}
	return 0, fmt.Errorf("filter did not terminate")
}

type policyABI struct {
	name                 string
	audit                uint32
	clone, socket, ioctl uint32
	refused              []uint32
	ordinary             []uint32
}

func policyABIs() []policyABI {
	return []policyABI{
		{"amd64", 0xc000003e, 56, 41, 16,
			[]uint32{272, 308, 165, 166, 155, 429, 428, 430, 432, 431, 433, 442,
				250, 248, 249, 321, 298, 323, 425, 426, 427, 246, 320, 175, 313, 176, 101, 310, 311, 438},
			[]uint32{0, 1, 2, 3, 9, 39, 57, 58, 59, 60, 61, 202, 231, 257, 291}},
		{"arm64", 0xc00000b7, 220, 198, 29,
			[]uint32{97, 268, 40, 39, 41, 429, 428, 430, 432, 431, 433, 442,
				219, 217, 218, 280, 241, 282, 425, 426, 427, 104, 294, 105, 273, 106, 117, 270, 271, 438},
			[]uint32{63, 64, 56, 57, 222, 172, 221, 93, 94, 260, 98, 281, 20}},
	}
}

func assertFilter(t *testing.T, abi policyABI, nr, want uint32, arguments ...uint64) {
	t.Helper()
	program, err := SeccompProgram(abi.name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runFilter(program, nr, abi.audit, arguments...)
	if err != nil || got != want {
		t.Fatalf("%s syscall %d args %x: action %#x, want %#x, error %v", abi.name, nr, arguments, got, want, err)
	}
}

func TestProcessSeccompRefusesKernelAndNamespaceOperations(t *testing.T) {
	for _, abi := range policyABIs() {
		t.Run(abi.name, func(t *testing.T) {
			for _, number := range abi.refused {
				assertFilter(t, abi, number, 0x00050001)
			}
			assertFilter(t, abi, 435, 0x00050026)
			for _, number := range abi.ordinary {
				assertFilter(t, abi, number, 0x7fff0000)
			}
			// New namespaces cannot hide among ordinary thread flags.
			for _, flag := range []uint64{0x00020000, 0x02000000, 0x04000000, 0x08000000,
				0x10000000, 0x20000000, 0x40000000} {
				assertFilter(t, abi, abi.clone, 0x00050001, flag)
				assertFilter(t, abi, abi.clone, 0x00050001, flag|0x00000100|17)
			}
			assertFilter(t, abi, abi.clone, 0x7fff0000, 17)
			assertFilter(t, abi, abi.clone, 0x7fff0000, 0x00000100|0x00000800|0x00010000)
		})
	}
}

func TestProcessSeccompKillsForeignABIAndRefusesX32(t *testing.T) {
	for _, abi := range policyABIs() {
		program, err := SeccompProgram(abi.name)
		if err != nil {
			t.Fatal(err)
		}
		for _, audit := range []uint32{0, 0x40000003, 0x40000028, abi.audit ^ 0x80000000} {
			got, err := runFilter(program, 0, audit)
			if err != nil || got != 0x80000000 {
				t.Errorf("%s allows ABI %#x: action %#x, error %v", abi.name, audit, got, err)
			}
		}
		if abi.name == "amd64" {
			for _, number := range []uint32{0, 56, 272, 435} {
				assertFilter(t, abi, number|0x40000000, 0x00050026)
			}
		}
	}
	if _, err := SeccompProgram("386"); err == nil {
		t.Fatal("built a launcher policy for an unsupported architecture")
	}
}

func TestProcessSeccompAllowsOnlyListedSocketFamiliesAndRouteProtocol(t *testing.T) {
	for _, abi := range policyABIs() {
		for family := uint64(0); family < 64; family++ {
			want := uint32(0x00050001)
			if family == 1 || family == 2 || family == 10 || family == 16 {
				want = 0x7fff0000
			}
			assertFilter(t, abi, abi.socket, want, family, 1, 0)
			// socket arguments are int: their upper word is ignored by
			// Linux, and cannot turn a denied family into an allowed one.
			assertFilter(t, abi, abi.socket, want, family|0x1122334400000000, 1, 0)
		}
		for protocol := uint64(0); protocol < 64; protocol++ {
			want := uint32(0x00050001)
			if protocol == 0 {
				want = 0x7fff0000
			}
			assertFilter(t, abi, abi.socket, want, 16, 3, protocol)
			assertFilter(t, abi, abi.socket, want, 16, 3, protocol|0xaabbccdd00000000)
		}
	}
}

func TestProcessSeccompBlocksTerminalInjectionWithRequestAliases(t *testing.T) {
	for _, abi := range policyABIs() {
		for _, request := range []uint64{0x5412, 0x541c, 0x1122334400005412, 0xaabbccdd0000541c} {
			assertFilter(t, abi, abi.ioctl, 0x00050001, 0, request)
		}
		for _, request := range []uint64{0x5401, 0x5402, 0x5413, 0x5414, 0x8913} {
			assertFilter(t, abi, abi.ioctl, 0x7fff0000, 0, request)
		}
	}
}

func FuzzProcessSocketPolicy(f *testing.F) {
	for _, seed := range [][2]uint64{{1, 0}, {2, 0}, {10, 6}, {16, 0}, {16, 9}, {40, 0},
		{0xffffffff00000001, 0}, {16, 0xabcdef0000000000}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, family, protocol uint64) {
		want := uint32(0x00050001)
		switch uint32(family) {
		case 1, 2, 10:
			want = 0x7fff0000
		case 16:
			if uint32(protocol) == 0 {
				want = 0x7fff0000
			}
		}
		for _, abi := range policyABIs() {
			assertFilter(t, abi, abi.socket, want, family, 1, protocol)
		}
	})
}
