//go:build !windows

package client_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/paths"
)

// TestWalledPasteImagePDFAndZipPolicy proves spec 7.6 for walled sessions:
// Ctrl+V of a PNG image pastes a guest path that the agent can read; a dropped
// PDF passes; a dropped .zip is refused by default unless [sandbox] paste =
// "any"; and private files stay refused even when paste = "any".
func TestWalledPasteImagePDFAndZipPolicy(t *testing.T) {
	r := newAttachRun(t)
	r.wall = "plain"

	dir := t.TempDir()
	pngData := "\x89PNG\r\n\x1a\nfake-png-body"
	pngFile := filepath.Join(dir, "clip.png")
	writeFile(t, pngFile, pngData)
	t.Setenv(brand.Env("TEST_CLIPBOARD_IMAGE"), pngFile)

	pdfData := "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"
	pdfFile := filepath.Join(dir, "report.pdf")
	writeFile(t, pdfFile, pdfData)

	zipData := "PK\x03\x04fake-zip-archive"
	zipFile := filepath.Join(dir, "bundle.zip")
	writeFile(t, zipFile, zipData)

	got := filepath.Join(r.h.Home, "pasted")
	id := r.session("stty raw -echo; printf '\\033[?2004hREADY'; exec cat > " + got)
	done := r.attach(context.Background(), id, client.DefaultSettings())
	r.term.WaitFor("READY")

	// 1. Ctrl+V of an image pastes a guest path that the session can read.
	r.term.Type("\x16")
	b := waitFile(t, got, "\x1b[201~")
	m := regexp.MustCompile(`\x1b\[200~(\S+) \x1b\[201~`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("Ctrl+V pasted %q, want bracketed guest path", b)
	}
	guestPNG := string(m[1])
	wantPasteDir := filepath.Join(paths.GuestRuntimeDir(id), "paste")
	if filepath.Dir(guestPNG) != wantPasteDir {
		t.Fatalf("Ctrl+V guest path = %q, want under %q", guestPNG, wantPasteDir)
	}
	if data, err := os.ReadFile(guestPNG); err != nil || string(data) != pngData {
		t.Fatalf("guest PNG content = %q (err=%v), want %q", data, err, pngData)
	}
	r.term.WaitFor("uploaded clipboard.png")

	// 2. Dropped PDF passes and uploads into the guest paste folder.
	r.term.Type("\x1b[200~" + pdfFile + "\x1b[201~")
	b = waitFile(t, got, "report.pdf \x1b[201~")
	matches := regexp.MustCompile(`\x1b\[200~(\S+report\.pdf) \x1b\[201~`).FindSubmatch(b)
	if matches == nil {
		t.Fatalf("dropped PDF pasted %q, want guest path", b)
	}
	guestPDF := string(matches[1])
	if filepath.Dir(guestPDF) != wantPasteDir {
		t.Fatalf("PDF guest path = %q, want under %q", guestPDF, wantPasteDir)
	}
	if data, err := os.ReadFile(guestPDF); err != nil || string(data) != pdfData {
		t.Fatalf("guest PDF content = %q (err=%v), want %q", data, err, pdfData)
	}

	// 3. Dropped .zip is refused by default; paste goes in as plain text.
	zipPaste := "\x1b[200~" + zipFile + "\x1b[201~"
	r.term.Type(zipPaste + "\x00")
	b = waitFile(t, got, "\x00")
	if !bytes.Contains(b, []byte(zipPaste+"\x00")) {
		t.Fatalf("refused .zip did not pass through as plain text: %q", b)
	}
	r.term.WaitFor("is not an image (PNG, JPEG, GIF, WebP) or PDF file")

	// Confirm the host paste folder stayed unused and guest paste dir has only PNG + PDF.
	noUploads(t, id)
	ents, err := os.ReadDir(wantPasteDir)
	if err != nil || len(ents) != 2 {
		t.Fatalf("guest paste dir has %d entries (err=%v), want 2 (PNG and PDF)", len(ents), err)
	}

	r.term.Type(detach)
	r.end(done)

	// 4. With [sandbox] paste = "any", .zip uploads while private .env stays refused.
	envFile := filepath.Join(dir, ".env")
	writeFile(t, envFile, "SECRET=1\n")
	got2 := filepath.Join(r.h.Home, "pasted-any")
	id2 := r.session("stty raw -echo; printf '\\033[?2004hREADY2'; exec cat > " + got2)
	settings := client.DefaultSettings()
	settings.Sandbox.Paste = "any"
	r.term.Clear()
	done2 := r.attach(context.Background(), id2, settings)
	r.term.WaitFor("READY2")

	r.term.Type("\x1b[200~" + zipFile + "\x1b[201~")
	b2 := waitFile(t, got2, "bundle.zip \x1b[201~")
	zipMatch := regexp.MustCompile(`\x1b\[200~(\S+bundle\.zip) \x1b\[201~`).FindSubmatch(b2)
	if zipMatch == nil || filepath.Dir(string(zipMatch[1])) != filepath.Join(paths.GuestRuntimeDir(id2), "paste") {
		t.Fatalf("paste=any .zip pasted %q, want guest path", b2)
	}
	if data, err := os.ReadFile(string(zipMatch[1])); err != nil || string(data) != zipData {
		t.Fatalf("guest .zip content = %q (err=%v), want %q", data, err, zipData)
	}

	envPaste := "\x1b[200~" + envFile + "\x1b[201~"
	r.term.Type(envPaste + "\x00")
	b2 = waitFile(t, got2, "\x00")
	if !strings.Contains(string(b2), envPaste+"\x00") {
		t.Fatalf("private .env was not refused under paste=any: %q", b2)
	}
	r.term.WaitFor("has a private name (.env)")

	r.term.Type(detach)
	r.end(done2)
}
