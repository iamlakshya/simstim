package wall

import "fmt"

// Level is a wall's strength. A higher level is stronger.
type Level int

// The wall levels, from weakest to strongest.
const (
	None Level = iota
	Process
	GVisor
	MicroVM
)

// ParseLevel reads none, process, gvisor or microvm.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "none":
		return None, nil
	case "process":
		return Process, nil
	case "gvisor":
		return GVisor, nil
	case "microvm":
		return MicroVM, nil
	default:
		return None, fmt.Errorf("unknown wall level %q", s)
	}
}

// String returns the level's name, as users type it.
func (l Level) String() string {
	switch l {
	case None:
		return "none"
	case Process:
		return "process"
	case GVisor:
		return "gvisor"
	case MicroVM:
		return "microvm"
	default:
		return "unknown"
	}
}
