package api

import "fmt"

// InputLimits reads already resolved policy through the canonical size parser.
// Copy bytes are aggregate per start. Seed bytes apply to EACH native bundle.
// Invalid explicit settings refuse, including an explicit empty string.
func InputLimits(values SandboxValues) (copyMax, seedMax int64, err error) {
	copyMax, seedMax = DefaultCopyMax, DefaultSeedMax
	for _, field := range []struct {
		name   string
		value  *string
		target *int64
	}{{"copy_max", values.CopyMax, &copyMax}, {"seed_max", values.SeedMax, &seedMax}} {
		if field.value == nil {
			continue
		}
		size, parseErr := ParseSize(*field.value)
		if parseErr != nil || size <= 0 {
			return 0, 0, fmt.Errorf("invalid sandbox.%s. Try: use a positive size such as 64M or 512M", field.name)
		}
		*field.target = size
	}
	return copyMax, seedMax, nil
}
