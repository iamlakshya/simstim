// Package secretmem protects the current trusted value-holding process.
// It changes no host setting and inspects no other process.
package secretmem

// State contains actual current-process protections. Dumpable is absent
// where the operating system has no Linux dumpability flag.
type State struct {
	Dumpable *int   `json:"dumpable,omitempty"`
	CoreSoft uint64 `json:"core_soft"`
	CoreHard uint64 `json:"core_hard"`
}
