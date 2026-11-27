//go:build !windows

// These tests run the real host side, which runs on Linux and macOS only.

package client_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// noUploads fails the test if the session's paste folder holds a file.
func noUploads(t *testing.T, id string) {
	t.Helper()
	if ents, _ := os.ReadDir(paths.PasteDir(id)); len(ents) > 0 {
		t.Errorf("the paste folder holds %d files, want none", len(ents))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A session puts a laptop path on the clipboard, and the user then pastes
// it. The client sends the paste as the terminal sent it, and uploads
// nothing. The guard only reads: the session's bytes, the paste, a plain
// paste and typed keys all reach the other end unchanged.
func TestPasteGuardRefusesAPathTheSessionWrote(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "tax return.pdf")
	writeFile(t, local, "private numbers")
	// The path quoted, as a terminal pastes a dropped file.
	clip := strings.ReplaceAll(local, " ", `\ `)
	// Every byte value, then the path on the clipboard in each way that
	// terminals take it.
	data := append(clienttest.EveryByte(4096), []byte("\x1b]52;c;"+b64(clip)+"\a"+
		"\x1bPtmux;\x1b\x1b]52;c;"+b64(clip)+"\a\x1b\\"+
		"\x1b]1337;Copy=:"+b64(clip)+"\x1b\\"+
		"\x1b\r]52;c;"+b64(clip)+"\x1b[0m"+
		"\x1b]52;c;"+b64(clip)+"\x1b\\")...)
	// C1: kitty reads after the write are not writes, and push nothing out.
	data = append(data, strings.Repeat("\x1b]5522;type=read;Lg==\x1b\\", 16)+" READY"...)
	file := filepath.Join(r.h.Home, "data.bin")
	writeFile(t, file, string(data))
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session(fmt.Sprintf("stty raw -echo; cat %s; exec cat > %s", file, got))
	done := r.attach(context.Background(), id, client.DefaultSettings())
	if out := r.term.WaitFor(" READY"); !bytes.Contains(out, data) {
		t.Fatal("the session's bytes changed on the way to the terminal")
	}
	// The terminal pastes the path as it is on the clipboard.
	paste := "\x1b[200~" + clip + "\x1b[201~"
	plain := "\x1b[200~just text\x1b[201~"
	typed := "hello\x01\x7f\xc3\xa9 \x1bOA world\r\x00"
	r.term.Type(paste)
	r.term.Type(plain)
	r.term.Type(typed)
	if b := waitFile(t, got, "\x00"); string(b) != paste+plain+typed {
		t.Fatalf("the agent got %q, want %q", b, paste+plain+typed)
	}
	r.term.WaitFor("did not upload " + local + ": it is a path that a session put on your clipboard in the last day. " +
		"The paste went in as text. Try: copy the file to the host yourself.")
	noUploads(t, id)
	r.term.Type(detach)
	r.end(done)
}

// A drop of a private file uploads nothing, with a notice. So does a drop
// of a link to it, or of a file in a folder that links to ~/.ssh.
func TestPasteGuardRefusesPrivateFiles(t *testing.T) {
	r := newAttachRun(t)
	key := filepath.Join(r.h.Home, ".ssh", "id_ed25519")
	writeFile(t, key, "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	other := t.TempDir()
	link, folder := filepath.Join(other, "shot.png"), filepath.Join(other, "keys")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(key), folder); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session("stty raw -echo; printf '\\033[?2004hREADY'; exec cat > " + got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	var want string
	for _, p := range []string{key, link, filepath.Join(folder, "id_ed25519")} {
		paste := "\x1b[200~" + p + "\x1b[201~"
		r.term.Type(paste)
		want += paste
	}
	r.term.Type("\x00")
	if b := waitFile(t, got, "\x00"); string(b) != want+"\x00" {
		t.Fatalf("the agent got %q, want %q", b, want+"\x00")
	}
	out := r.term.WaitFor("did not upload ~/.ssh/id_ed25519: it is in ~/.ssh, a private folder. The paste went in as text. " +
		"Try: copy the file to the host yourself.")
	if n := strings.Count(string(out), "is in ~/.ssh, a private folder"); n != 3 {
		t.Errorf("%d notices of a private folder, want 3: %q", n, out)
	}
	noUploads(t, id)
	r.term.Type(detach)
	r.end(done)
}

// A dropped image uploads, with a notice that names it and its size.
func TestDroppedImageNotice(t *testing.T) {
	r := newAttachRun(t)
	png := filepath.Join(t.TempDir(), "shot.png")
	writeFile(t, png, "\x89PNG\r\n\x1a\nnot really")
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session("stty raw -echo; printf '\\033[?2004hREADY'; exec cat > " + got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.term.Type("\x1b[200~" + png + "\x1b[201~")
	b := waitFile(t, got, "\x1b[201~")
	m := regexp.MustCompile(`\x1b\[200~(\S+) \x1b\[201~`).FindSubmatch(b)
	if m == nil || filepath.Dir(string(m[1])) != paths.PasteDir(id) {
		t.Fatalf("the agent got %q, want the uploaded file's path", b)
	}
	r.term.WaitFor("\x1b]777;notify;")
	r.term.WaitFor("uploaded " + png + " (18 bytes)")
	r.term.Type(detach)
	r.end(done)
}

// A path that one session wrote is refused when it is pasted into another
// session, after the client restarts: the memory is a file that every
// client process shares.
func TestPasteGuardRemembersAcrossSessions(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "notes.txt")
	writeFile(t, local, "notes")
	first := r.session("stty raw -echo; printf '\\033]52;c;" + b64(local) + "\\007WROTE'; exec sleep 60")
	done := r.attach(context.Background(), first, client.DefaultSettings())
	r.term.WaitFor("WROTE")
	r.term.Type(detach)
	r.end(done)
	client.ForgetClipMemory() // as a new client process would start

	got := filepath.Join(r.h.Home, "pasted")
	second := r.session("stty raw -echo; printf '\\033[?2004hREADY'; exec cat > " + got)
	done = r.attach(context.Background(), second, client.DefaultSettings())
	r.term.WaitFor("READY")
	paste := "\x1b[200~" + local + "\x1b[201~"
	r.term.Type(paste + "\x00")
	if b := waitFile(t, got, "\x00"); string(b) != paste+"\x00" {
		t.Fatalf("the agent got %q, want %q", b, paste+"\x00")
	}
	noUploads(t, second)
	if out := r.term.Output(); !strings.Contains(string(out), "did not upload") {
		t.Errorf("no notice: %q", out)
	}
	r.term.Type(detach)
	r.end(done)
}

// V1: kitty's protocol with a repeated type, read then wdata. The terminal
// takes the last, so the packet carries data, and the paste is refused.
func TestPasteGuardKittyRepeatedType(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "notes.txt")
	writeFile(t, local, "notes")
	out := "\x1b]5522;type=write\x1b\\\x1b]5522;type=read:type=wdata:mime=dGV4dC9wbGFpbg==;" + b64(local) +
		"\x1b\\\x1b]5522;type=wdata\x1b\\\x1b[?2004hREADY"
	file := filepath.Join(r.h.Home, "data.bin")
	writeFile(t, file, out)
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session(fmt.Sprintf("stty raw -echo; cat %s; exec cat > %s", file, got))
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	paste := "\x1b[200~" + local + "\x1b[201~"
	r.term.Type(paste + "\x00")
	if b := waitFile(t, got, "\x00"); string(b) != paste+"\x00" {
		t.Fatalf("the agent got %q, want %q", b, paste+"\x00")
	}
	r.term.WaitFor("it is a path that a session put on your clipboard")
	noUploads(t, id)
	r.term.Type(detach)
	r.end(done)
}

// V10: the upload reads the file that the guard checked, through its open
// file, not the path again.
func TestDropReadsTheCheckedFile(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "shot.png")
	writeFile(t, local, "checked")
	defer client.SetDropRead(func() {
		// The path now names another file.
		if err := os.Rename(local, local+".old"); err != nil {
			t.Error(err)
		}
		writeFile(t, local, "swapped")
	})()
	got := filepath.Join(r.h.Home, "pasted")
	id := r.session("stty raw -echo; printf '\\033[?2004hREADY'; exec cat > " + got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.term.Type("\x1b[200~" + local + "\x1b[201~")
	b := waitFile(t, got, "\x1b[201~")
	m := regexp.MustCompile(`\x1b\[200~(\S+) \x1b\[201~`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("the agent got %q, want the uploaded file's path", b)
	}
	if up, _ := os.ReadFile(string(m[1])); string(up) != "checked" {
		t.Fatalf("the upload holds %q, want the file that the guard checked", up)
	}
	r.term.Type(detach)
	r.end(done)
}

// V9: a drop of twenty files while the session's output stops inside a
// sequence. The notice waits for the sequence to end, and names every file.
func TestDropNoticeNamesEveryFile(t *testing.T) {
	r := newAttachRun(t)
	dir := t.TempDir()
	var drop []string
	for i := range 20 {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.png", i))
		writeFile(t, p, "png")
		drop = append(drop, p)
	}
	signal := filepath.Join(r.h.Home, "signal")
	id := r.session("stty raw -echo; printf '\\033[?2004hREADY\\033[38;2;1'; while [ ! -f " + signal +
		" ]; do sleep 0.05; done; printf ';2;3mDONE'; exec sleep 60")
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.term.Type("\x1b[200~" + strings.Join(drop, " ") + "\x1b[201~")
	hostWait(t, id, 20)
	if bytes.Contains(r.term.Output(), []byte("uploaded")) {
		t.Fatal("a notice cut the session's sequence")
	}
	writeFile(t, signal, "")
	out := r.term.WaitFor("f19.png (3 bytes)")
	i := bytes.Index(out, []byte("DONE"))
	if i < 0 {
		t.Fatalf("the session did not end its sequence: %q", out)
	}
	tail := out[i:]
	for _, p := range drop {
		if !bytes.Contains(tail, []byte(filepath.Base(p)+" (3 bytes)")) {
			t.Errorf("the notice does not name %s", filepath.Base(p))
		}
	}
	r.term.Type(detach)
	r.end(done)
}

// hostWait waits until the session's paste folder holds n files.
func hostWait(t *testing.T, id string, n int) {
	t.Helper()
	for range 400 {
		if ents, _ := os.ReadDir(paths.PasteDir(id)); len(ents) >= n {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the paste folder never held %d files", n)
}

// sessionPrinting starts a session that prints out, then turns on
// bracketed paste, prints READY, and keeps what it is sent in got.
func (r *attachRun) sessionPrinting(t *testing.T, out, got string) string {
	t.Helper()
	return r.sessionPrintingLater(t, out, "", got)
}

// sessionPrintingLater is sessionPrinting, which prints later more than a
// second after out, so the writes in later are a second newer.
func (r *attachRun) sessionPrintingLater(t *testing.T, out, later, got string) string {
	t.Helper()
	first, second := filepath.Join(t.TempDir(), "out.bin"), filepath.Join(t.TempDir(), "later.bin")
	writeFile(t, first, out)
	writeFile(t, second, later+"\x1b[?2004hREADY")
	wait := ""
	if later != "" {
		wait = "sleep 1.2; "
	}
	return r.session(fmt.Sprintf("stty raw -echo; cat %s; %scat %s; exec cat > %s", first, wait, second, got))
}

// pasteRefused pastes local into the attached session, and checks that the
// session got the paste as the terminal sent it, with a refusal notice.
func (r *attachRun) pasteRefused(t *testing.T, id, local, got string) {
	t.Helper()
	paste := "\x1b[200~" + local + "\x1b[201~"
	r.term.Type(paste + "\x00")
	if b := waitFile(t, got, "\x00"); string(b) != paste+"\x00" {
		t.Fatalf("the agent got %q, want %q", b, paste+"\x00")
	}
	r.term.WaitFor("did not upload")
	noUploads(t, id)
}

// W1: a kitty field holds a letter with the byte 0x9C. The pinned terminal
// reads it as text, and puts the path on the clipboard.
func TestPasteGuard9CInsideALetter(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "notes.txt")
	writeFile(t, local, "notes")
	got := filepath.Join(r.h.Home, "pasted")
	id := r.sessionPrinting(t, "\x1b]5522;type=write\x1b\\\x1b]5522;type=wdata:loc=\u00dc:mime=dGV4dC9wbGFpbg==;"+b64(local)+
		"\x1b\\\x1b]5522;type=wdata\x1b\\", got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.pasteRefused(t, id, local, got)
	r.term.Type(detach)
	r.end(done)
}

// W2: the pinned terminal sends selection "x" to the clipboard. 300 writes
// to the primary selection after it push nothing of the clipboard's out.
//
// V3: xterm may send "s" to the clipboard too, so the same holds for "s".
func TestPasteGuardUnknownSelection(t *testing.T) {
	for _, sel := range []string{"x", "s"} {
		t.Run(sel, func(t *testing.T) {
			r := newAttachRun(t)
			local := filepath.Join(t.TempDir(), "notes.txt")
			writeFile(t, local, "notes")
			// The primary writes come a second later, so they are the newest:
			// a list of selections that held the first write would push it out.
			var later string
			for i := range 300 {
				later += "\x1b]52;p;" + b64(fmt.Sprintf("/tmp/primary/%d", i)) + "\a"
			}
			got := filepath.Join(r.h.Home, "pasted")
			id := r.sessionPrintingLater(t, "\x1b]52;"+sel+";"+b64(local)+"\a", later, got)
			done := r.attach(context.Background(), id, client.DefaultSettings())
			r.term.WaitFor("READY")
			// The journal holds every write until the goroutine folds it into
			// the file, which keeps the newest 256 of each list. Fold it first.
			client.FlushClipMemory()
			r.pasteRefused(t, id, local, got)
			r.term.Type(detach)
			r.end(done)
		})
	}
}

// W4: a kitty transfer that runs across attaches and client processes,
// through the attach's own detach. One process sees its start, and the next
// process sees data with no start, so it lost track of the write. That
// holds across its next detach and attach, where the commit comes, and the
// paste is refused.
//
// V4, round 5: a third process, which saw no part of the transfer, refuses
// the paste too: the second shared that it lost track of a write.
func TestPasteGuardKittyAcrossProcesses(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "notes.txt")
	writeFile(t, local, "notes")
	data := b64(local)
	packet := "\x1b]5522;type=wdata:mime=dGV4dC9wbGFpbg==;"
	for _, out := range []string{
		"\x1b]5522;type=write\x1b\\" + packet + data[:12] + "\x1b\\", // the start, in one process
		packet + data[12:] + "\x1b\\",                                // the rest, in the next process
	} {
		id := r.sessionPrinting(t, out, filepath.Join(t.TempDir(), "unused"))
		r.term.Clear()
		done := r.attach(context.Background(), id, client.DefaultSettings())
		r.term.WaitFor("READY")
		r.term.Type(detach)
		r.end(done)
		if !strings.Contains(out, "type=write") {
			continue
		}
		client.ForgetClipMemory() // the next attach is in a new client process
	}
	r.failedAttach(t) // V4: an attach that fails keeps the state for the next
	got := filepath.Join(r.h.Home, "pasted")
	id := r.sessionPrinting(t, "\x1b]5522;type=wdata\x1b\\", got) // the commit, after a detach in this process
	r.term.Clear()
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.pasteRefused(t, id, local, got)
	r.term.Type(detach)
	r.end(done)
	client.ForgetClipMemory() // the third process
	got = filepath.Join(r.h.Home, "pasted-third")
	id = r.sessionPrinting(t, "", got)
	r.term.Clear()
	done = r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.pasteRefused(t, id, local, got)
	r.term.Type(detach)
	r.end(done)
}

// failedAttach attaches to a session that does not exist.
func (r *attachRun) failedAttach(t *testing.T) {
	t.Helper()
	r.term.Clear()
	if e := r.end(r.attach(context.Background(), "nosuchsession", client.DefaultSettings())); e.err == nil {
		t.Fatal("an attach to no session did not fail")
	}
}

// V4: a write that the reader lost track of keeps every drop refused in
// the next attach of the process, also after an attach that failed. The
// selection here is longer than the reader reads, with no data after it.
func TestPasteGuardLostWriteSurvivesAFailedAttach(t *testing.T) {
	r := newAttachRun(t)
	id := r.sessionPrinting(t, "\x1b]52;"+strings.Repeat("c", 40)+"\a", filepath.Join(t.TempDir(), "unused"))
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.term.Type(detach)
	r.end(done)
	r.failedAttach(t)
	png := filepath.Join(t.TempDir(), "shot.png")
	writeFile(t, png, "png")
	got := filepath.Join(r.h.Home, "pasted")
	id = r.sessionPrinting(t, "", got)
	r.term.Clear()
	done = r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.pasteRefused(t, id, png, got)
	r.term.Type(detach)
	r.end(done)
}

// V1: xterm with C1 controls on takes an OSC 52 that starts with a C1
// control in ground, as a raw byte or in UTF-8.
func TestPasteGuardC1InGround(t *testing.T) {
	for name, form := range map[string][2]string{"raw": {"\x9d", "\x9c"}, "UTF-8": {"\u009d", "\u009c"}} {
		t.Run(name, func(t *testing.T) {
			r := newAttachRun(t)
			local := filepath.Join(t.TempDir(), "notes.txt")
			writeFile(t, local, "notes")
			got := filepath.Join(r.h.Home, "pasted")
			id := r.sessionPrinting(t, "x"+form[0]+"52;c;"+b64(local)+form[1], got)
			done := r.attach(context.Background(), id, client.DefaultSettings())
			r.term.WaitFor("READY")
			r.pasteRefused(t, id, local, got)
			r.term.Type(detach)
			r.end(done)
		})
	}
}

// V1, round 5: W1's sequence, ESC, a letter with 0x9D, and "]", which only
// xterm's reading takes as an OSC 52. The session writes it a byte at a
// time, after the attach, so each byte comes in a frame of its own.
func TestPasteGuardEscBeforeALetter(t *testing.T) {
	r := newAttachRun(t)
	local := filepath.Join(t.TempDir(), "notes.txt")
	writeFile(t, local, "notes")
	out := "\x1b\u00dd]52;c;" + b64(local) + "\a"
	dir := t.TempDir()
	for i := range len(out) {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("%04d", i)), out[i:i+1])
	}
	got := filepath.Join(r.h.Home, "pasted")
	ready := filepath.Join(t.TempDir(), "ready.bin")
	writeFile(t, ready, "\x1b[?2004hREADY")
	id := r.session(fmt.Sprintf("stty raw -echo; sleep 1; for f in %s/*; do cat \"$f\"; sleep 0.01; done; cat %s; exec cat > %s",
		dir, ready, got))
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")
	r.pasteRefused(t, id, local, got)
	r.term.Type(detach)
	r.end(done)
}

// Layer 1: a paste is not a drag. A paste that is the clipboard's text, or
// the primary selection's, uploads nothing, whatever wrote it. A drag,
// while the clipboard holds other text, uploads.
func TestPasteOfClipboardTextIsNotADrag(t *testing.T) {
	for _, c := range []struct {
		name, clipboard, primary string
		upload                   bool
	}{
		{"the clipboard holds it", "PATH", "", false},
		{"the primary selection holds it", "other text", "PATH", false},
		// V2, round 5: each text that a read returns counts on its own.
		{"the clipboard cannot be read, and the primary selection holds it", "", "PATH", false},
		{"a drag", "other text", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAttachRun(t)
			png := filepath.Join(t.TempDir(), "shot.png")
			writeFile(t, png, "png")
			clip := filepath.Join(t.TempDir(), "clipboard") // no file: the clipboard cannot be read
			if c.clipboard != "" {
				writeFile(t, clip, strings.ReplaceAll(c.clipboard, "PATH", png)+"\n")
			}
			t.Setenv(brand.Env("TEST_CLIPBOARD_TEXT"), clip)
			if c.primary != "" {
				primary := filepath.Join(t.TempDir(), "primary")
				writeFile(t, primary, strings.ReplaceAll(c.primary, "PATH", png))
				t.Setenv(brand.Env("TEST_PRIMARY_TEXT"), primary)
			}
			got := filepath.Join(r.h.Home, "pasted")
			id := r.sessionPrinting(t, "", got)
			done := r.attach(context.Background(), id, client.DefaultSettings())
			r.term.WaitFor("READY")
			if !c.upload {
				r.pasteRefused(t, id, png, got)
				r.term.WaitFor("it is the text on your clipboard, not a drag")
			} else {
				r.term.Type("\x1b[200~" + png + "\x1b[201~")
				b := waitFile(t, got, "\x1b[201~")
				if m := regexp.MustCompile(`\x1b\[200~(\S+) \x1b\[201~`).FindSubmatch(b); m == nil || filepath.Dir(string(m[1])) != paths.PasteDir(id) {
					t.Fatalf("a drag did not upload: the agent got %q", b)
				}
			}
			r.term.Type(detach)
			r.end(done)
		})
	}
}
