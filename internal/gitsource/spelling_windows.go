//go:build windows

package gitsource

import (
	"path/filepath"
	"strings"
)

// The walker accepts ordinary drive/UNC paths, never a device namespace,
// alternate stream or Win32 component alias. Go's rooted native handles own
// resolution; callers cannot select a different namespace with path spelling.
func validateSourceSpelling(name string) error {
	value := strings.ReplaceAll(name, "/", `\`)
	if strings.HasPrefix(value, `\\?\`) || strings.HasPrefix(value, `\\.\`) || strings.HasPrefix(value, `\??\`) {
		return metadataRefused()
	}
	volume := filepath.VolumeName(value)
	if volume != "" && !filepath.IsAbs(value) {
		return metadataRefused()
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, volume), `\`) {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if strings.ContainsAny(part, ":\x00\r\n") || strings.TrimRight(part, " .") != part {
			return metadataRefused()
		}
		base, _, _ := strings.Cut(strings.ToUpper(part), ".")
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
			return metadataRefused()
		}
	}
	return nil
}
