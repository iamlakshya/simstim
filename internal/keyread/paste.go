package keyread

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/muesli/cancelreader"
)

// MaxPaste is the most of a paste that a Paste keeps. The rest is read, to
// its end, and dropped.
const MaxPaste = 1 << 20

// pasteBlock is the most that one read of a paste takes, and the size of
// a Reader's one read buffer. A paste is read in blocks, not a byte at a
// time, so that a paste of a megabyte takes a moment. What a block holds
// after the paste's end stays for the next key.
const pasteBlock = 32 << 10

// pasteEnd ends a bracketed paste.
const pasteEnd = "\x1b[201~"

// paste reads a bracketed paste after its ESC [200~, to its ESC [201~,
// however long it takes, in blocks: a line break and esc in it are text.
// Ctrl+C is the way out of a paste whose end never comes: a terminal that
// brackets a paste drops ctrl+c from it, so one there comes from the
// keyboard, and the key is ctrl+c, a cancel. A paste keeps at most
// MaxPaste bytes. Bytes after its end, or after a ctrl+c, stay for the next
// key.
func (r *Reader) paste(ctx context.Context) (Key, error) {
	var text, tail []byte
	end := []byte(pasteEnd)
	for {
		chunk, err := r.block(ctx, pasteBlock)
		if err != nil {
			return Key{}, err
		}
		// tail is the end of the last block, which may start the end mark.
		buf := slices.Concat(tail, chunk)
		stop := bytes.IndexByte(buf, 0x03)
		at := bytes.Index(buf, end)
		switch {
		case stop >= 0 && (at < 0 || stop < at):
			r.unread(buf[stop+1:]...)
			return Key{Kind: CtrlC}, nil
		case at >= 0:
			text = appendPaste(text, buf[:at]...)
			r.unread(buf[at+len(end):]...)
			return Key{Kind: Paste, Text: string(text)}, nil
		}
		keep := min(len(end)-1, len(buf))
		text = appendPaste(text, buf[:len(buf)-keep]...)
		tail = append([]byte(nil), buf[len(buf)-keep:]...)
	}
}

// OneLine is the text of a paste as a one-line field takes it: only what a
// typed key gives, the runes that unicode.IsPrint takes. So it has no line
// break, U+2028 and U+2029 too, and no control or hidden character: no
// bidi control, zero-width character or tag. It has no U+FFFD either: not
// a pasted one, nor one for a byte that is no rune, as of a rune that
// MaxPaste cut. A pasted line break never ends a line or picks a row.
func OneLine(text string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, text)
}

// appendPaste adds b to a paste's text, up to MaxPaste bytes.
func appendPaste(text []byte, b ...byte) []byte {
	if room := MaxPaste - len(text); room < len(b) {
		b = b[:max(room, 0)]
	}
	return append(text, b...)
}

// block returns what earlier reads left, or the bytes of the next read, up
// to n, as soon as there are any.
func (r *Reader) block(ctx context.Context, n int) ([]byte, error) {
	r.mu.Lock()
	if len(r.back) > 0 {
		b := r.back
		r.back = nil
		r.mu.Unlock()
		return b, nil
	}
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("read the terminal: %w", cancelreader.ErrCanceled)
	}
	r.start(n)
	r.mu.Unlock()
	select {
	case res := <-r.res:
		return r.took(res)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
