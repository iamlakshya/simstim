package onboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The PATH hint adds the folder that the host named to PATH, as text: a
// quote, $( ) or a backtick in it runs nothing, in the command or later in
// ~/.profile (verify9 V8).
func TestPathHintQuotesTheFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hosts are Linux")
	}
	if got := PathHint("/home/dev/.local/bin"); got != `on the host, add it to PATH: echo 'export PATH="/home/dev/.local/bin:$PATH"' >> ~/.profile` {
		t.Errorf("a plain folder: %s", got)
	}
	for _, dir := range []string{
		"/home/dev/o'connor/.local/bin",
		"/home/dev/$(touch RAN)/bin",
		"/home/dev/`touch RAN`/bin",
		"/home/dev/a b/bin",
		`/home/dev/"q"/bin`,
		`/home/dev/\t\n\c/bin`,
	} {
		home := t.TempDir()
		hint := PathHint(dir)
		cmd, ok := strings.CutPrefix(hint, "on the host, add it to PATH: ")
		if !ok {
			t.Fatalf("%s: %s", dir, hint)
		}
		run := exec.Command("sh", "-c", cmd+` && . ~/.profile && printf %s "$PATH"`)
		run.Dir, run.Env = home, []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := run.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), dir+":") {
			t.Errorf("%s: the hint %s gave PATH %q, %v", dir, cmd, out, err)
		}
		if _, err := os.Stat(filepath.Join(home, "RAN")); err == nil {
			t.Errorf("%s: the hint ran a command", dir)
		}
	}
}
