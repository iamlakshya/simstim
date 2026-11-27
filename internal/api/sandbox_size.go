package api

import "fmt"

// SandboxSize is the actual size selected for one start, in processors and bytes.
type SandboxSize struct {
	CPUs   int   `json:"cpus,omitempty"`
	Memory int64 `json:"memory,omitempty"`
}

// DefaultSandboxSize is the fallback when neither a template nor agent data
// specifies the size. It is unrelated to a host's machine minimum.
func DefaultSandboxSize() SandboxSize { return SandboxSize{CPUs: 2, Memory: 4 << 30} }

// ResolveSandboxSize applies the resolved session layer to template, agent
// and common defaults. It does not mutate policy or perform admission.
// Callers use the returned size only when starting a stopped sandbox.
func ResolveSandboxSize(values SandboxValues, template, agent *SandboxSize) (SandboxSize, error) {
	size := DefaultSandboxSize()
	for _, defaults := range []*SandboxSize{agent, template} {
		if defaults == nil {
			continue
		}
		if defaults.CPUs < 0 || defaults.Memory < 0 {
			return SandboxSize{}, fmt.Errorf("invalid sandbox default size. Try: correct the template or agent data")
		}
		if defaults.CPUs != 0 {
			size.CPUs = defaults.CPUs
		}
		if defaults.Memory != 0 {
			size.Memory = defaults.Memory
		}
	}
	if values.CPUs != nil {
		if *values.CPUs < 0 {
			return SandboxSize{}, fmt.Errorf("sandbox processors cannot be negative. Try: pass a positive --cpus value")
		}
		if *values.CPUs != 0 {
			size.CPUs = *values.CPUs
		}
	}
	if values.Memory != nil && *values.Memory != "" {
		memory, err := ParseSize(*values.Memory)
		if err != nil || memory < MinMemoryMax {
			return SandboxSize{}, fmt.Errorf("invalid sandbox memory size. Try: pass --memory with a size of at least 256M")
		}
		size.Memory = memory
	}
	if size.CPUs <= 0 || size.Memory < MinMemoryMax {
		return SandboxSize{}, fmt.Errorf("sandbox default size is too small. Try: correct the template or agent data")
	}
	return size, nil
}
