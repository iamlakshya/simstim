//go:build unix && !linux

package secretmem

func setNonDumpable() error          { return nil }
func currentDumpable() (*int, error) { return nil, nil }
