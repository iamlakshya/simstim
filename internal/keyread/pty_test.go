//go:build darwin || linux

package keyread_test

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	xterm "golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// counted is a terminal's reader that counts its reads.
type counted struct {
	cancelreader.CancelReader
	reads *atomic.Int64
}

func (c counted) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.CancelReader.Read(p)
}

// A paste of a megabyte on a real terminal is one key, read in blocks:
// a read takes what the terminal has, about a KiB on macOS and 4 KiB on
// Linux, not a byte. The test counts the reads, not the time, which a busy
// machine stretches: a megabyte must take fewer than one read for each 64
// bytes. A read of a byte at a time takes a million.
func TestABigPasteIsReadInBlocks(t *testing.T) {
	term := hosttest.NewTerminal(t, 80, 10)
	fd := int(term.TTY.Fd())
	old, err := xterm.MakeRaw(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = xterm.Restore(fd, old) }()
	var reads atomic.Int64
	open := keyread.Open
	keyread.Open = func(f io.Reader) (cancelreader.CancelReader, error) {
		r, err := open(f)
		if err != nil {
			return nil, err
		}
		return counted{r, &reads}, nil
	}
	defer func() { keyread.Open = open }()
	r, err := keyread.For(term.TTY)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", keyread.MaxPaste)
	go term.Type("\x1b[200~" + big + "\x1b[201~")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	k, err := r.ReadKey(ctx)
	if n := reads.Load(); err != nil || k.Kind != keyread.Paste || len(k.Text) != len(big) || n > int64(keyread.MaxPaste/64) {
		t.Errorf("a paste of %d bytes: %v, %d bytes, %v, in %d reads", len(big), k.Kind, len(k.Text), err, n)
	}
}
