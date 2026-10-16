package client

import "github.com/iamlakshya/simstim/internal/api"

// MemoryAuto is a NewArgs.MemoryMax that asks for the host's default even
// when config.toml names a size, as `new --memory-max auto` does. NewSession
// sends it as 0.
const MemoryAuto = -1

// ParseMemoryFlag reads the size of `new --memory-max`: "auto" for the host's
// default (MemoryAuto), or a size such as "4G".
func ParseMemoryFlag(v string) (int64, error) {
	n, err := api.ParseMemoryMax(v)
	if err != nil {
		return 0, &UserError{Msg: "--memory-max: " + err.Error(), Hint: "Pass auto, or a size of 256M or more: " + api.MemorySizes + "."}
	}
	if n == 0 {
		return MemoryAuto, nil
	}
	return n, nil
}

// SettingsMemoryMax is [sessions] memory_max from s, in bytes, or 0 for the
// host's default.
func SettingsMemoryMax(s Settings) (int64, error) {
	n, err := api.ParseMemoryMax(s.Sessions.MemoryMax)
	if err != nil {
		return 0, &UserError{Msg: "memory_max in config.toml: " + err.Error(),
			Hint: `Set [sessions] memory_max to "auto", or to a size of 256M or more (` + api.MemorySizes + "), in " + settingsPath() + "."}
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
