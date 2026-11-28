package client

import (
	"bytes"
	"fmt"
	"strings"
)

// AllowedPasteContent reports whether data starts with the signature of an
// image (PNG, JPEG, GIF, WebP) or a PDF file (spec 7.6, question 19).
func AllowedPasteContent(data []byte) bool {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return true
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return true
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return true
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return true
	default:
		return false
	}
}

// pasteTypeAllowed reports whether data may be pasted or dropped into a
// session. Unwalled sessions keep their existing behaviour; walled sessions
// allow only images and PDF files unless [sandbox] paste = "any" (spec 7.6).
func pasteTypeAllowed(walled bool, setting string, data []byte) bool {
	if !walled {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(setting), "any") {
		return true
	}
	return AllowedPasteContent(data)
}

func pasteTypeNotice(paths []string, bad string) string {
	what := "it"
	if len(paths) > 1 {
		what = tildePath(bad)
	}
	files := tildePath(paths[0])
	if len(paths) > 1 {
		files = fmt.Sprintf("%d files", len(paths))
	}
	return fmt.Sprintf(
		`did not upload %s: %s is not an image (PNG, JPEG, GIF, WebP) or PDF file. The paste went in as text. Try: set paste = "any" under [sandbox] in config.toml.`,
		files, what,
	)
}
