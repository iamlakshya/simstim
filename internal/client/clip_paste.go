package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.design/x/clipboard"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
)

// A paste is not a drag (spec 7.6). A terminal sends a file that the user
// drags onto it, and the text that the user pastes, the same way: as a
// paste of the text. A drag puts nothing on the clipboard. So when a paste
// that names files is the text on the local clipboard, or on the primary
// selection, it is a paste of that text, wherever the text came from, and
// the client uploads nothing. This needs no parsing of any terminal's
// output. A write to the clipboard between the paste and this read can
// still make a paste look like a drag: the hash check of the paste guard
// covers that for the readings that it names.

// clipboardTexts returns every text that a read returned: the local
// clipboard's and, on Linux, the primary selection's. Each counts on its
// own, so the primary selection counts when the clipboard cannot be read,
// and the other way round. No text means that neither could be read.
//
// For tests, <NAME>_TEST_CLIPBOARD_TEXT names a file that holds the
// clipboard's text, and <NAME>_TEST_PRIMARY_TEXT one that holds the primary
// selection's, so a test never touches the user's clipboard. A missing
// file is a read that failed.
func clipboardTexts(ctx context.Context) []string {
	var texts []string
	if p, set := os.LookupEnv(brand.Env("TEST_CLIPBOARD_TEXT")); set {
		for _, f := range []string{p, os.Getenv(brand.Env("TEST_PRIMARY_TEXT"))} {
			if b, err := os.ReadFile(f); f != "" && err == nil { //nolint:gosec // G703: a file the test chose, read only in tests
				texts = append(texts, string(b))
			}
		}
		return texts
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	board, primary := false, false
	if err := clipboard.Init(); err == nil {
		if b, err := clipboard.Read(ctx, clipboard.FmtText); err == nil {
			texts, board = append(texts, string(b)), true
		}
		if runtime.GOOS == "linux" {
			if b, err := clipboard.Read(ctx, clipboard.FmtText, clipboard.FromPrimary()); err == nil {
				texts, primary = append(texts, string(b)), true
			}
		}
	}
	if runtime.GOOS != "linux" {
		return texts
	}
	// Linux desktops where the library cannot read one of them: the tools.
	if !board {
		if t, ok := toolText(ctx, []string{"wl-paste", "--no-newline"}, []string{"xclip", "-selection", "clipboard", "-o"}); ok {
			texts = append(texts, t)
		}
	}
	if !primary {
		if t, ok := toolText(ctx, []string{"wl-paste", "--primary", "--no-newline"}, []string{"xclip", "-selection", "primary", "-o"}); ok {
			texts = append(texts, t)
		}
	}
	return texts
}

// toolText returns the output of the first tool that is there and works.
func toolText(ctx context.Context, tools ...[]string) (string, bool) {
	for _, argv := range tools {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		if out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output(); err == nil {
			return string(out), true
		}
	}
	return "", false
}

// pastedFromClipboard reports whether a paste is the text on the local
// clipboard or the primary selection, after the unquoting and trimming
// that a drop's paths get. When neither can be read, it is false: the hash
// check alone applies, and the attach says so once in its debug log.
func (a *attacher) pastedFromClipboard(ctx context.Context, text []byte) bool {
	texts := clipboardTexts(ctx)
	if len(texts) == 0 {
		if !a.clipUnread {
			a.clipUnread = true
			DebugLog().Debug("paste guard cannot read the clipboard: the hash check alone applies", diag.CatKey, diag.CatAttach)
		}
		return false
	}
	paste := pasteWords(string(text))
	for _, t := range texts {
		if len(paste) > 0 && slices.Equal(pasteWords(t), paste) {
			return true
		}
	}
	return false
}

// pasteWords is a paste's words, as localFiles reads them. A terminal may
// paste a line end as CR, so CR counts as a line end too.
func pasteWords(s string) []string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return splitShellWords(strings.TrimSpace(s))
}

// clipboardNotice says that a paste of clipboard text that names files went
// in as text, and that a drag uploads a file.
func clipboardNotice(paths []string) string {
	files, what := tildePath(paths[0]), "a file"
	if len(paths) > 1 {
		files, what = fmt.Sprintf("%d files", len(paths)), "files"
	}
	return fmt.Sprintf("did not upload %s: the paste names %s, but it is the text on your clipboard, not a drag. "+
		"The paste went in as text. Try: drag the file onto the terminal window to upload it.", files, what)
}
