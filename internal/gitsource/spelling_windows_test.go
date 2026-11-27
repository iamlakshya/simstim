//go:build windows

package gitsource

import "testing"

func TestWindowsSourceSpellingRejectsNamespaceStreamAndAliases(t *testing.T) {
	for _, path := range []string{`\\?\C:\repo`, `\\.\C:\repo`, `\??\C:\repo`, `C:relative`, `C:\repo:stream`, `C:\repo.`, `C:\repo `, `C:\NUL\repo`, `C:\COM1.txt\repo`} {
		if err := validateSourceSpelling(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	for _, path := range []string{`C:\repo`, `\\server\share\repo`, `..\repo`, `C:\.git\objects`} {
		if err := validateSourceSpelling(path); err != nil {
			t.Errorf("ordinary source %q: %v", path, err)
		}
	}
}
