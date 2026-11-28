package client

import "github.com/iamlakshya/simstim/internal/api"

// MemoryAuto is a NewArgs.MemoryMax that asks for the host's default even
// when config.toml names a size, as `new --memory-max auto` does. NewSession
// sends it as 0. A resume sends it as it is (api.MemoryAuto).
const MemoryAuto = api.MemoryAuto

// ParseMemoryFlag reads the size of `new --memory-max` or `--memory`: "auto"
// for the host's default (MemoryAuto), or a size such as "4G".
func ParseMemoryFlag(v string) (int64, error) {
	return ParseMemoryFlagNamed("--memory-max", v)
}

// ParseMemoryFlagNamed parses a memory flag with the caller's flag name in
// error messages.
func ParseMemoryFlagNamed(flagName, v string) (int64, error) {
	n, err := api.ParseMemoryMax(v)
	if err != nil {
		return 0, &UserError{Msg: flagName + ": " + err.Error(), Hint: "Pass auto, or a size of 256M or more: " + api.MemorySizes + ".", Code: "bad_value", Kind: KindUsage}
	}
	if n == 0 {
		return MemoryAuto, nil
	}
	return n, nil
}

// SandboxSizes holds resolved session size defaults from Settings and Host.
type SandboxSizes struct {
	CPUs     int
	Memory   int64
	DiskMax  int64
	IdleStop string
	CopyMax  int64
	SeedMax  int64
}

// ResolveSandboxSizes merges [sandbox] settings from config.toml and hosts.toml
// (host layer wins over global config).
func ResolveSandboxSizes(s Settings, h Host) (SandboxSizes, error) {
	out := SandboxSizes{
		CPUs:    s.Sandbox.CPUs,
		CopyMax: DefaultCopyInMaxBytes,
		SeedMax: DefaultSeedMaxBytes,
	}
	memRaw := s.Sandbox.Memory
	diskRaw := s.Sandbox.DiskMax
	idleRaw := s.Sandbox.IdleStop
	if h.Sandbox != nil {
		if h.Sandbox.CPUs < 0 {
			return out, &UserError{Msg: "sandbox.cpus in hosts.toml must be positive", Hint: "Set sandbox.cpus to a positive whole number.", Code: "bad_config", Kind: KindUsage}
		}
		if h.Sandbox.CPUs > 0 {
			out.CPUs = h.Sandbox.CPUs
		}
		if h.Sandbox.Memory != "" {
			memRaw = h.Sandbox.Memory
		}
		if h.Sandbox.DiskMax != "" {
			diskRaw = h.Sandbox.DiskMax
		}
		if h.Sandbox.IdleStop != "" {
			idleRaw = h.Sandbox.IdleStop
		}
	}
	if out.CPUs < 0 {
		return out, &UserError{Msg: "sandbox.cpus in config must be positive", Hint: "Set sandbox.cpus to a positive whole number.", Code: "bad_config", Kind: KindUsage}
	}
	if memRaw != "" {
		n, err := api.ParseMemoryMax(memRaw)
		if err != nil {
			return out, &UserError{Msg: "sandbox.memory in config: " + err.Error(), Hint: `Set sandbox.memory to "auto" or a size of 256M or more (` + api.MemorySizes + ").", Code: "bad_config", Kind: KindUsage}
		}
		if n == 0 {
			out.Memory = MemoryAuto
		} else {
			out.Memory = n
		}
	}
	if diskRaw != "" {
		n, err := api.ParseByteSize(diskRaw, 1<<20)
		if err != nil {
			return out, &UserError{Msg: "sandbox.disk_max in config: " + err.Error(), Hint: "Set sandbox.disk_max to a size such as 10G.", Code: "bad_config", Kind: KindUsage}
		}
		out.DiskMax = n
	}
	if idleRaw != "" {
		if err := CheckIdleStop(idleRaw); err != nil {
			return out, &UserError{Msg: "sandbox.idle_stop in config: " + err.Error(), Hint: "Set sandbox.idle_stop to a duration of a minute or more (such as 30m or 2h), or never.", Code: "bad_config", Kind: KindUsage}
		}
		out.IdleStop = idleRaw
	}
	if s.Sandbox.CopyMax != "" {
		n, err := api.ParseByteSize(s.Sandbox.CopyMax, 1<<10)
		if err != nil {
			return out, &UserError{Msg: "sandbox.copy_max in config.toml: " + err.Error(), Hint: "Set [sandbox] copy_max to a size such as 64M.", Code: "bad_config", Kind: KindUsage}
		}
		out.CopyMax = n
	}
	if s.Sandbox.SeedMax != "" {
		n, err := api.ParseByteSize(s.Sandbox.SeedMax, 1<<20)
		if err != nil {
			return out, &UserError{Msg: "sandbox.seed_max in config.toml: " + err.Error(), Hint: "Set [sandbox] seed_max to a size such as 512M.", Code: "bad_config", Kind: KindUsage}
		}
		out.SeedMax = n
	}
	return out, nil
}

// SettingsMemoryMax is [sessions] memory_max from s, in bytes, or 0 for the
// host's default.
func SettingsMemoryMax(s Settings) (int64, error) {
	n, err := api.ParseMemoryMax(s.Sessions.MemoryMax)
	if err != nil {
		return 0, &UserError{Msg: "memory_max in config.toml: " + err.Error(),
			Hint: `Set [sessions] memory_max to "auto", or to a size of 256M or more (` + api.MemorySizes + "), in " + settingsPath() + ".", Code: "bad_config", Kind: KindUsage}
	}
	return n, nil
}

// withMemoryMax fills a's memory cap from config.toml when the caller left it
// at 0. It turns MemoryAuto into the 0 that hosts read as their default.
func withMemoryMax(a NewArgs) (NewArgs, error) {
	switch {
	case a.MemoryMax < 0:
		a.MemoryMax = 0
	case a.MemoryMax == 0:
		n, err := SettingsMemoryMax(LoadSettings())
		if err != nil {
			return a, err
		}
		a.MemoryMax = n
	}
	return a, nil
}
