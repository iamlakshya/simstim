package guest

import (
	"fmt"
	"os"
	"path/filepath"
)

// BrowserHelperPath returns the path of the guest BROWSER helper script inside
// runDir (isolation spec 17.2).
func BrowserHelperPath(runDir string) string {
	return filepath.Join(runDir, "browser")
}

const browserHelperScript = `#!/bin/sh
if [ $# -gt 0 ]; then
  printf '\nOpen in your browser: %s\n' "$1" >/dev/tty 2>/dev/null || printf '\nOpen in your browser: %s\n' "$1"
fi
`

func writeBrowserHelper(runDir string) error {
	p := BrowserHelperPath(runDir)
	if err := os.WriteFile(p, []byte(browserHelperScript), 0o700); err != nil { //nolint:gosec // G306: helper must be executable
		return fmt.Errorf("write guest browser helper: %w", err)
	}
	return nil
}
