package process

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/brand"
)

// LandlockConfig lists the paths allowed once Landlock is enforced inside the
// process-wall sandbox. Paths outside these rules cannot be written (and
// outside RODirs cannot be read).
type LandlockConfig struct {
	RODirs  []string `json:"ro_dirs,omitempty"`
	RWDirs  []string `json:"rw_dirs,omitempty"`
	RWFiles []string `json:"rw_files,omitempty"`
}

// LandlockEnvVar is the environment variable carrying the JSON-encoded
// LandlockConfig into guest init so it can apply Landlock before reading fd 3.
func LandlockEnvVar() string { return brand.Env("WALL_LANDLOCK") }

// EncodeLandlockEnv serialises cfg for passing via LandlockEnvVar().
func EncodeLandlockEnv(cfg LandlockConfig) (string, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encode landlock config: %w", err)
	}
	return string(b), nil
}

// LandlockFromEnv decodes LandlockConfig from the process environment. ok is
// false when the variable is unset (for example in the unwalled plain driver).
func LandlockFromEnv() (cfg LandlockConfig, ok bool, err error) {
	raw := os.Getenv(LandlockEnvVar())
	if raw == "" {
		return LandlockConfig{}, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return LandlockConfig{}, true, fmt.Errorf("decode %s: %w. Try: restart the session", LandlockEnvVar(), err)
	}
	return cfg, true, nil
}
