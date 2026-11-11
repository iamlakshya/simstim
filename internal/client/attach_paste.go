package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/proto"
)

// sendUpload uploads a file and pastes its remote path into the session. A
// notice names the file and its size.
func (a *attacher) sendUpload(ctx context.Context, name string, data []byte) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	path, err := a.conn.Upload(ctx, a.o.Session, name, data)
	if err != nil {
		a.notify("upload failed: " + CleanLine(err.Error()))
		return
	}
	a.notify(uploadNotice([]string{name}, []int{len(data)}))
	a.pasteText([]byte(path + " "))
}

// pasteText sends text the way a paste would arrive.
func (a *attacher) pasteText(t []byte) {
	if a.tracker.M.Private[2004] {
		t = append(append([]byte("\x1b[200~"), t...), []byte("\x1b[201~")...)
	}
	_ = a.conn.Send(proto.TInput, t)
}

// paste forwards a bracketed paste. When it is only local file paths (a file
// dropped on the terminal), the files are uploaded and the paths rewritten,
// with one notice that names each file. If the paste guard refuses a file,
// nothing is uploaded, and the paste goes to the session as the terminal
// sent it.
func (a *attacher) paste(ctx context.Context, p keys.Piece) {
	files := localFiles(p.Text)
	if len(files) == 0 {
		_ = a.conn.Send(proto.TInput, p.Raw)
		return
	}
	// A paste of the clipboard's text is not a drag, whatever wrote it.
	if a.pastedFromClipboard(ctx, p.Text) {
		a.notify(clipboardNotice(files))
		_ = a.conn.Send(proto.TInput, p.Raw)
		return
	}
	drop, msg := a.guard.check(p.Text, files)
	if msg != "" {
		a.notify(msg)
		_ = a.conn.Send(proto.TInput, p.Raw)
		return
	}
	defer drop.close()
	if dropRead != nil {
		dropRead()
	}
	var remote, names []string
	var sizes []int
	// The notice names each file that went up, even when a later one fails.
	defer func() {
		if len(names) > 0 {
			a.notify(uploadNotice(names, sizes))
		}
	}()
	for i, f := range files {
		// Read through the file that the guard checked, not the path again.
		data, ok, err := readDropped(drop.files[i], MaxImage)
		if err != nil || !ok {
			_ = a.conn.Send(proto.TInput, p.Raw)
			return
		}
		uctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		path, err := a.conn.Upload(uctx, a.o.Session, filepath.Base(f), data)
		cancel()
		if err != nil {
			a.notify("upload failed: " + CleanLine(err.Error()))
			_ = a.conn.Send(proto.TInput, p.Raw)
			return
		}
		names, sizes = append(names, tildePath(f)), append(sizes, len(data))
		remote = append(remote, shellQuote(path))
	}
	a.pasteText([]byte(strings.Join(remote, " ") + " "))
}

// dropRead, when set, runs after the guard opened the files of a drop and
// before the client reads them. Tests use it to change the path just there.
var dropRead func()

// localFiles returns the paths in t if every token is an existing local regular file.
func localFiles(t []byte) []string {
	toks := splitShellWords(strings.TrimSpace(string(t)))
	if len(toks) == 0 || len(toks) > 20 {
		return nil
	}
	for _, s := range toks {
		if !filepath.IsAbs(s) {
			return nil
		}
		st, err := os.Stat(s)
		if err != nil || !st.Mode().IsRegular() {
			return nil
		}
	}
	return toks
}

// splitShellWords splits on unescaped spaces, handling backslash escapes and quotes,
// the way terminals quote dropped paths.
func splitShellWords(s string) []string {
	var out []string
	var cur bytes.Buffer
	var quote rune
	esc, have := false, false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc, have = false, true
		case r == '\\' && quote != '\'':
			esc = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, have = r, true
		case r == ' ' || r == '\n' || r == '\t':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func shellQuote(p string) string {
	if strings.ContainsAny(p, " '\"\\$`") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}
