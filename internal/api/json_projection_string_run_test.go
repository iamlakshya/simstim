package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stringRunReader struct {
	reader             io.ReaderAt
	calls, asked, read int
	failAt             int
}

var errStringRunRead = errors.New("injected original string source failure")

func (r *stringRunReader) ReadAt(out []byte, offset int64) (int, error) {
	r.calls++
	r.asked += len(out)
	if r.failAt != 0 && r.calls == r.failAt {
		return 0, errStringRunRead
	}
	n, err := r.reader.ReadAt(out, offset)
	r.read += n
	return n, err
}

// This is the former scalar loop, kept only as an out-of-band unit oracle.
// It shares the original cursor/source/window implementation with the run.
func referenceStringBytes(c *jsonCursor) error {
	if err := c.expect('"'); err != nil {
		return err
	}
	for {
		b, err := c.take()
		if err != nil {
			return err
		}
		switch {
		case b == '"':
			return nil
		case b == '\\':
			b, err = c.take()
			if err != nil {
				return err
			}
			if b == 'u' {
				for range 4 {
					b, err := c.take()
					if err != nil {
						return err
					}
					if hexJSON(b) < 0 {
						return ErrJSONProjection
					}
				}
			} else if b != '"' && b != '\\' && b != '/' && b != 'b' && b != 'f' && b != 'n' && b != 'r' && b != 't' {
				return ErrJSONProjection
			}
		case b < 0x20:
			return ErrJSONProjection
		}
	}
}

func stringRunFD(t *testing.T, raw []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original-json-source")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestJSONStringWindowRunKeepsNativeGrammarFDReadsAndExactCacheClock(t *testing.T) {
	inputs := [][]byte{
		[]byte(`""`), []byte(`"ordinary ASCII"`), []byte(`"a\\b\"c\n\u0000\ud800\udc00"`),
		[]byte(`"` + strings.Repeat("x", 4094) + `\u0061` + strings.Repeat("y", 16387) + `"`),
		[]byte(`"` + strings.Repeat("x", 4095) + `é` + strings.Repeat("z", 4097) + `"`),
		append(append([]byte(`"`), bytes.Repeat([]byte{0xff}, 4097)...), '"'),
		[]byte(`"` + strings.Repeat("a", 4096) + `\q"`), []byte(`"\u12xz"`), []byte(`"unterminated`),
		append(append([]byte(`"`), bytes.Repeat([]byte{'a'}, 4095)...), 0x1f, '"'),
	}
	for index, raw := range inputs {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			file := stringRunFD(t, raw)
			leftReader, rightReader := &stringRunReader{reader: file}, &stringRunReader{reader: file}
			left, err := NewJSONProjection(t.Context(), leftReader, int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			right, err := NewJSONProjection(t.Context(), rightReader, int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			original, run := left.cursor(0), right.cursor(0)
			before, after := referenceStringBytes(&original), run.stringValue()
			var decoded string
			native := json.Unmarshal(raw, &decoded)
			if (before == nil) != (native == nil) || (after == nil) != (before == nil) || original.pos != run.pos || left.clock != right.clock || *left.windows != *right.windows {
				t.Fatal("native grammar, failure position or exact original cache/clock changed", before, after, native)
			}
			if leftReader.calls != rightReader.calls || leftReader.asked != rightReader.asked || leftReader.read != rightReader.read {
				t.Fatal("the run changed existing FD reads or window sizes")
			}
			memory, err := NewJSONBytesProjection(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			cursor := memory.cursor(0)
			if err := cursor.stringValue(); (err == nil) != (native == nil) || memory.windows != nil || cursor.pos != run.pos {
				t.Fatal("byte-backed grammar allocated windows or diverged from the native result", err)
			}
			if native == nil {
				equal, err := right.StringEqual(JSONSpan{End: int64(len(raw))}, decoded)
				if err != nil || !equal {
					t.Fatal("Unicode/escape decoding diverged from encoding/json", err)
				}
			}
		})
	}
}

type stringRunCancelContext struct {
	context.Context
	at, calls int
	cancel    context.CancelFunc
	cursor    *jsonCursor
	position  int64
	clock     uint64
}

func (c *stringRunCancelContext) Err() error {
	c.calls++
	if c.calls >= c.at {
		if c.cursor != nil {
			c.position, c.clock = c.cursor.pos, c.cursor.p.clock
		}
		c.cancel()
	}
	return c.Context.Err()
}

func TestJSONStringWindowRunPreservesEveryByteCancellationAndReadErrors(t *testing.T) {
	raw := []byte(`"` + strings.Repeat("x", 20000) + `"`)
	file := stringRunFD(t, raw)
	for _, at := range []int{2, 3, 10, 4096, 4097, 4098, 8194, 16387} {
		firstBase, firstCancel := context.WithCancel(t.Context())
		defer firstCancel()
		secondBase, secondCancel := context.WithCancel(t.Context())
		defer secondCancel()
		firstCtx := &stringRunCancelContext{Context: firstBase, cancel: firstCancel, at: at}
		secondCtx := &stringRunCancelContext{Context: secondBase, cancel: secondCancel, at: at}
		first, err := NewJSONProjection(firstCtx, file, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		second, err := NewJSONProjection(secondCtx, file, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		original, run := first.cursor(0), second.cursor(0)
		firstCtx.cursor, secondCtx.cursor = &original, &run
		before, after := referenceStringBytes(&original), run.stringValue()
		if !errors.Is(before, context.Canceled) || !errors.Is(after, context.Canceled) || firstCtx.calls != secondCtx.calls || firstCtx.position != secondCtx.position || firstCtx.clock != secondCtx.clock || *first.windows != *second.windows {
			t.Fatal("the run widened the original byte cancellation/position/clock", at, before, after)
		}
	}
	firstReader, secondReader := &stringRunReader{reader: file, failAt: 2}, &stringRunReader{reader: file, failAt: 2}
	first, _ := NewJSONProjection(t.Context(), firstReader, int64(len(raw)))
	second, _ := NewJSONProjection(t.Context(), secondReader, int64(len(raw)))
	before, after := first.cursor(0), second.cursor(0)
	if err := referenceStringBytes(&before); !errors.Is(err, errStringRunRead) {
		t.Fatal(err)
	}
	if err := after.stringValue(); !errors.Is(err, errStringRunRead) || before.pos != after.pos || first.clock != second.clock || firstReader.calls != secondReader.calls {
		t.Fatal("the run remapped a real source failure or changed its clock", err)
	}
}
