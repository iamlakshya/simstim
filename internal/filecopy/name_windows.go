//go:build windows

package filecopy

import "strings"

func platformName(name string) error {
	if strings.Contains(name, ":") || strings.TrimRight(name, " .") != name {
		return failure("cp_path", "a copy name is a Windows stream or component alias")
	}
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return failure("cp_path", "a copy name denotes a Windows device")
	}
	return nil
}
