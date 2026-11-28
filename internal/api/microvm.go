package api

import "fmt"

// Root filesystem persistence modes for sandbox.system on a microvm
// (isolation spec 6.6, 9.1).
const (
	SandboxSystemReset   = "reset"
	SandboxSystemKeep    = "keep"
	DefaultSandboxSystem = SandboxSystemReset

	SandboxSystemHint = `Set sandbox.system = "reset" or "keep" in config.toml`
)

// SandboxSystemMessage returns the error message for an invalid sandbox.system
// value, without a trailing Try: hint.
func SandboxSystemMessage(raw string) string {
	return fmt.Sprintf("sandbox.system is %q, and it must be reset or keep.", raw)
}

// ParseSandboxSystem validates sandbox.system and applies the built-in default
// ("reset") when raw is empty.
func ParseSandboxSystem(raw string) (string, error) {
	if raw == "" {
		return DefaultSandboxSystem, nil
	}
	switch raw {
	case SandboxSystemReset, SandboxSystemKeep:
		return raw, nil
	default:
		return "", fmt.Errorf("%s Try: %s", SandboxSystemMessage(raw), SandboxSystemHint)
	}
}
