package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
)

// ErrJSONProjection marks invalid JSON or an ambiguous object. The caller
// retains ownership of the reader and supplies a stable, locked snapshot.
var ErrJSONProjection = errors.New("invalid or ambiguous projected JSON")

// JSONSpan refers to encoded bytes in one projection's retained source.
type JSONSpan struct{ Start, End int64 }

// JSONField carries offsets, never a materialised name or value. Known is
// the index of the exact decoded name in Object's known list, or -1.
type JSONField struct {
	Name, Value JSONSpan
	Known       int
}

// JSONProjection validates and inspects JSON without retaining its bodies.
// It is a source-only prototype. Its grammar and resource parity are unproven.
// One owner may use nested visitors. Concurrent use is unsupported.
type JSONProjection struct {
	ctx  context.Context
	r    io.ReaderAt
	raw  []byte
	size int64
	// Windows belong to this one read, not to transient cursors. This
	// avoids a potentially escaping ReadAt buffer per field or row.
	windows *[4]jsonProjectionWindow
	clock   uint64
	// Hash assistance reuses one state and its buffers. It never decides
	// equality, and must not allocate one digest object for every field.
	nameDigest hash.Hash
	nameRune   [4]byte
	nameSum    [32]byte
	// Collision injection is per-instance and used only by package tests.
	testNameHash func(JSONSpan) ([32]byte, error)
	matchReader  jsonProjectionRuneReader
}

type jsonProjectionWindow struct {
	base   int64
	n      int
	used   uint64
	buffer [4096]byte
}

func NewJSONProjection(ctx context.Context, r io.ReaderAt, size int64) (*JSONProjection, error) {
	if ctx == nil || r == nil || size < 0 {
		return nil, ErrJSONProjection
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &JSONProjection{ctx: ctx, r: r, size: size, windows: new([4]jsonProjectionWindow), nameDigest: sha256.New()}, nil
}

// NewJSONBytesProjection borrows an existing in-memory source without file
// windows or a source copy. The owner must keep raw unchanged during use.
// Materialising adapters must independently copy any returned data.
func NewJSONBytesProjection(ctx context.Context, raw []byte) (*JSONProjection, error) {
	if ctx == nil {
		return nil, ErrJSONProjection
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &JSONProjection{ctx: ctx, raw: raw, size: int64(len(raw)), nameDigest: sha256.New()}, nil
}

func (p *JSONProjection) Root() (JSONSpan, error) {
	c := p.cursor(0)
	if err := c.space(); err != nil {
		return JSONSpan{}, err
	}
	start := c.pos
	if err := c.value(); err != nil {
		return JSONSpan{}, err
	}
	span := JSONSpan{start, c.pos}
	if err := c.space(); err != nil || c.pos != p.size {
		if err != nil {
			return JSONSpan{}, err
		}
		return JSONSpan{}, ErrJSONProjection
	}
	return span, nil
}

// ObjectRoot matches Decoder.Token('{') followed by Decode(RawMessage)
// per field, as JSONObjectFields does. Its value depth budget excludes
// the outer token object. Object must still apply duplicate/name rules.
func (p *JSONProjection) ObjectRoot() (JSONSpan, error) {
	c := p.cursor(0)
	if err := c.space(); err != nil {
		return JSONSpan{}, err
	}
	start := c.pos
	if err := c.expect('{'); err != nil {
		return JSONSpan{}, err
	}
	if err := c.space(); err != nil {
		return JSONSpan{}, err
	}
	b, err := c.peek()
	if err != nil {
		return JSONSpan{}, err
	}
	if b == '}' {
		c.pos++
	} else {
		for {
			if err := c.space(); err != nil {
				return JSONSpan{}, err
			}
			if err := c.stringValue(); err != nil {
				return JSONSpan{}, err
			}
			if err := c.space(); err != nil {
				return JSONSpan{}, err
			}
			if err := c.expect(':'); err != nil {
				return JSONSpan{}, err
			}
			if err := c.space(); err != nil {
				return JSONSpan{}, err
			}
			if err := c.value(); err != nil {
				return JSONSpan{}, err
			}
			if err := c.space(); err != nil {
				return JSONSpan{}, err
			}
			b, err := c.take()
			if err != nil {
				return JSONSpan{}, err
			}
			if b == '}' {
				break
			}
			if b != ',' {
				return JSONSpan{}, ErrJSONProjection
			}
		}
	}
	span := JSONSpan{start, c.pos}
	if err := c.space(); err != nil {
		return JSONSpan{}, err
	}
	if c.pos != p.size {
		return JSONSpan{}, ErrJSONProjection
	}
	return span, nil
}

func (p *JSONProjection) validSpan(span JSONSpan) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if span.Start < 0 || span.End <= span.Start || span.End > p.size {
		return ErrJSONProjection
	}
	return nil
}

func (p *JSONProjection) Literal(span JSONSpan, literal string) (bool, error) {
	if err := p.validSpan(span); err != nil {
		return false, err
	}
	if span.End-span.Start != int64(len(literal)) {
		return false, nil
	}
	c := p.cursor(span.Start)
	for i := range len(literal) {
		b, err := c.take()
		if err != nil {
			return false, err
		}
		if b != literal[i] {
			return false, nil
		}
	}
	return true, nil
}

// WalkEncoded visits exact source bytes in fixed-size pieces. A borrowed
// piece is invalid after the next projection read. Visitors must not retain
// it or read this projection while using it. No source bytes are rewritten.
func (p *JSONProjection) WalkEncoded(span JSONSpan, visit func([]byte) error) error {
	if err := p.validSpan(span); err != nil {
		return err
	}
	c := p.cursor(span.Start)
	if p.r == nil {
		for c.pos < span.End {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			n := min(int64(4096), span.End-c.pos)
			if err := visit(p.raw[c.pos : c.pos+n]); err != nil {
				return err
			}
			c.pos += n
		}
		return nil
	}
	if p.windows == nil {
		return ErrJSONProjection
	}
	for c.pos < span.End {
		if _, err := c.peek(); err != nil {
			return err
		}
		advanced := false
		for index := range *p.windows {
			window := &p.windows[index]
			if window.n > 0 && c.pos >= window.base && c.pos < window.base+int64(window.n) {
				offset := c.pos - window.base
				n := min(int64(window.n)-offset, span.End-c.pos)
				if err := visit(window.buffer[offset : offset+n]); err != nil {
					return err
				}
				c.pos += n
				advanced = true
				break
			}
		}
		if !advanced {
			return ErrJSONProjection
		}
	}
	return nil
}

func (p *JSONProjection) Array(span JSONSpan, visit func(JSONSpan) error) error {
	if err := p.validSpan(span); err != nil {
		return err
	}
	c := p.cursor(span.Start)
	if err := c.expect('['); err != nil {
		return err
	}
	if err := c.space(); err != nil {
		return err
	}
	if b, _ := c.peek(); b == ']' {
		c.pos++
		return c.ending(span.End)
	}
	for {
		if err := c.space(); err != nil {
			return err
		}
		start := c.pos
		if err := c.value(); err != nil {
			return err
		}
		if err := visit(JSONSpan{start, c.pos}); err != nil {
			return err
		}
		if err := c.space(); err != nil {
			return err
		}
		b, err := c.take()
		if err != nil {
			return err
		}
		if b == ']' {
			return c.ending(span.End)
		}
		if b != ',' {
			return ErrJSONProjection
		}
	}
}

type jsonCursor struct {
	p   *JSONProjection
	pos int64
}

func (p *JSONProjection) cursor(pos int64) jsonCursor {
	return jsonCursor{p: p, pos: pos}
}

func (c *jsonCursor) peek() (byte, error) {
	if err := c.p.ctx.Err(); err != nil {
		return 0, err
	}
	if c.pos < 0 || c.pos >= c.p.size {
		return 0, io.EOF
	}
	if c.p.r == nil {
		return c.p.raw[c.pos], nil
	}
	if c.p.windows == nil {
		return 0, ErrJSONProjection
	}
	var chosen *jsonProjectionWindow
	for index := range *c.p.windows {
		window := &c.p.windows[index]
		if window.n > 0 && c.pos >= window.base && c.pos < window.base+int64(window.n) {
			c.p.clock++
			window.used = c.p.clock
			return window.buffer[c.pos-window.base], nil
		}
		if chosen == nil || window.n == 0 || window.used < chosen.used {
			chosen = window
		}
	}
	{
		chosen.base = c.pos
		chosen.n = 0
		want := int(min(int64(len(chosen.buffer)), c.p.size-c.pos))
		n, err := c.p.r.ReadAt(chosen.buffer[:want], c.pos)
		if n == 0 {
			if err == nil {
				err = io.ErrNoProgress
			}
			return 0, err
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		chosen.n = n
		c.p.clock++
		chosen.used = c.p.clock
	}
	return chosen.buffer[c.pos-chosen.base], nil
}

func (c *jsonCursor) take() (byte, error) {
	b, err := c.peek()
	if err == nil {
		c.pos++
	}
	return b, err
}

func (c *jsonCursor) expect(want byte) error {
	b, err := c.take()
	if err != nil {
		return err
	}
	if b != want {
		return ErrJSONProjection
	}
	return nil
}

func (c *jsonCursor) ending(end int64) error {
	if err := c.p.ctx.Err(); err != nil {
		return err
	}
	if c.pos != end {
		return ErrJSONProjection
	}
	return nil
}

func (c *jsonCursor) space() error {
	for {
		b, err := c.peek()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return nil
		}
		c.pos++
	}
}
