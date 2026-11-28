package client

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

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
	MaxRun   time.Duration
}

type rawSandboxSizes struct {
	cpus   int
	mem    string
	disk   string
	idle   string
	maxRun string
}

func mergeRawSandboxSizes(s Settings, h Host) (rawSandboxSizes, error) {
	raw := rawSandboxSizes{
		cpus:   s.Sandbox.CPUs,
		mem:    s.Sandbox.Memory,
		disk:   s.Sandbox.DiskMax,
		idle:   s.Sandbox.IdleStop,
		maxRun: s.Sandbox.MaxRun,
	}
	if h.Sandbox != nil {
		if h.Sandbox.CPUs < 0 {
			return raw, &UserError{Msg: "sandbox.cpus in hosts.toml must be positive", Hint: "Set sandbox.cpus to a positive whole number.", Code: "bad_config", Kind: KindUsage}
		}
		if h.Sandbox.CPUs > 0 {
			raw.cpus = h.Sandbox.CPUs
		}
		if h.Sandbox.Memory != "" {
			raw.mem = h.Sandbox.Memory
		}
		if h.Sandbox.DiskMax != "" {
			raw.disk = h.Sandbox.DiskMax
		}
		if h.Sandbox.IdleStop != "" {
			raw.idle = h.Sandbox.IdleStop
		}
		if h.Sandbox.MaxRun != "" {
			raw.maxRun = h.Sandbox.MaxRun
		}
	}
	if raw.cpus < 0 {
		return raw, &UserError{Msg: "sandbox.cpus in config must be positive", Hint: "Set sandbox.cpus to a positive whole number.", Code: "bad_config", Kind: KindUsage}
	}
	return raw, nil
}

// ResolveSandboxSizes merges [sandbox] settings from config.toml and hosts.toml
// (host layer wins over global config).
func ResolveSandboxSizes(s Settings, h Host) (SandboxSizes, error) {
	raw, err := mergeRawSandboxSizes(s, h)
	if err != nil {
		return SandboxSizes{}, err
	}
	out := SandboxSizes{
		CPUs:    raw.cpus,
		CopyMax: DefaultCopyInMaxBytes,
		SeedMax: DefaultSeedMaxBytes,
	}
	if raw.mem != "" {
		n, err := api.ParseMemoryMax(raw.mem)
		if err != nil {
			return out, &UserError{Msg: "sandbox.memory in config: " + err.Error(), Hint: `Set sandbox.memory to "auto" or a size of 256M or more (` + api.MemorySizes + ").", Code: "bad_config", Kind: KindUsage}
		}
		if n == 0 {
			out.Memory = MemoryAuto
		} else {
			out.Memory = n
		}
	}
	if raw.disk != "" {
		n, err := api.ParseByteSize(raw.disk, 1<<20)
		if err != nil {
			return out, &UserError{Msg: "sandbox.disk_max in config: " + err.Error(), Hint: "Set sandbox.disk_max to a size such as 10G.", Code: "bad_config", Kind: KindUsage}
		}
		out.DiskMax = n
	}
	if raw.idle != "" {
		if err := CheckIdleStop(raw.idle); err != nil {
			return out, &UserError{Msg: "sandbox.idle_stop in config: " + err.Error(), Hint: "Set sandbox.idle_stop to a duration of a minute or more (such as 30m or 2h), or never.", Code: "bad_config", Kind: KindUsage}
		}
		out.IdleStop = raw.idle
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
	if raw.maxRun != "" {
		d, err := api.ParseRunDuration(raw.maxRun)
		if err != nil {
			return out, &UserError{Msg: "sandbox.max_run in config: " + err.Error(), Hint: `Set sandbox.max_run to a positive duration such as "4h" or "1m".`, Code: "bad_config", Kind: KindUsage}
		}
		out.MaxRun = d
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
