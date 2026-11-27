package process

import "fmt"

// Instruction is one classic BPF instruction in the compiled launcher policy.
// The generator writes these constants into the static launcher. No filter
// arrives from an untrusted request at run time.
type Instruction struct {
	Code   uint16
	JT, JF uint8
	K      uint32
}

const (
	load           = 0x20
	equal          = 0x15
	bits           = 0x45
	ret            = 0x06
	allow          = 0x7fff0000
	killProcess    = 0x80000000
	denied         = 0x00050001
	noSys          = 0x00050026
	namespaceFlags = 0x7e020000
)

// SeccompProgram builds the mandatory filter for a supported host ABI.
// The native architecture guard also rejects compat calls. x32 returns ENOSYS.
func SeccompProgram(arch string) ([]Instruction, error) {
	var audit, clone, socket, ioctl uint32
	var refused []uint32
	switch arch {
	case "amd64":
		audit, clone, socket, ioctl = 0xc000003e, 56, 41, 16
		refused = []uint32{272, 308, 165, 166, 155, 429, 428, 430, 432, 431, 433, 442, 250, 248, 249, 321, 298, 323, 425, 426, 427, 246, 320, 175, 313, 176, 101, 310, 311, 438}
	case "arm64":
		audit, clone, socket, ioctl = 0xc00000b7, 220, 198, 29
		refused = []uint32{97, 268, 40, 39, 41, 429, 428, 430, 432, 431, 433, 442, 219, 217, 218, 280, 241, 282, 425, 426, 427, 104, 294, 105, 273, 106, 117, 270, 271, 438}
	default:
		return nil, fmt.Errorf("process walls do not support architecture %q", arch)
	}
	p := []Instruction{{load, 0, 0, 4}, {equal, 1, 0, audit}, {ret, 0, 0, killProcess}, {load, 0, 0, 0}}
	if arch == "amd64" {
		p = append(p, Instruction{bits, 0, 1, 0x40000000}, Instruction{ret, 0, 0, noSys})
	}
	p = append(p, Instruction{equal, 0, 1, 435}, Instruction{ret, 0, 0, noSys})
	for _, nr := range refused {
		p = append(p, Instruction{equal, 0, 1, nr}, Instruction{ret, 0, 0, denied})
	}
	// All namespace flags fit the low word, which the kernel reads for clone.
	p = append(p, Instruction{equal, 0, 4, clone}, Instruction{load, 0, 0, 16}, Instruction{bits, 0, 1, namespaceFlags}, Instruction{ret, 0, 0, denied}, Instruction{ret, 0, 0, allow})
	// socket's family and protocol are int arguments. Match their low words,
	// including the harmless high-word garbage that the kernel ignores.
	p = append(p, Instruction{equal, 0, 9, socket}, Instruction{load, 0, 0, 16}, Instruction{equal, 7, 0, 1}, Instruction{equal, 6, 0, 2}, Instruction{equal, 5, 0, 10}, Instruction{equal, 0, 2, 16}, Instruction{load, 0, 0, 32}, Instruction{equal, 2, 0, 0}, Instruction{ret, 0, 0, denied}, Instruction{ret, 0, 0, allow}, Instruction{load, 0, 0, 0})
	// Prevent terminal injection into the outside terminal through an inherited
	// descriptor, including forms whose request has irrelevant upper bits.
	p = append(p, Instruction{equal, 0, 4, ioctl}, Instruction{load, 0, 0, 24}, Instruction{equal, 1, 0, 0x5412}, Instruction{equal, 0, 1, 0x541c}, Instruction{ret, 0, 0, denied}, Instruction{ret, 0, 0, allow})
	return p, nil
}
