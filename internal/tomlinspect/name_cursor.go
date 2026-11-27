package tomlinspect

import (
	"context"
	"io"
	"math/bits"
	"unicode/utf8"
)

// nameInput.literal is used only for immutable schema/default names. Only
// actual FD Text inputs may be inserted into the metadata name dictionary.
type nameInput struct {
	text    Text
	literal string
	static  bool
}
type nameBytes struct {
	input    nameInput
	text     textIterator
	at       int
	encoded  [4]byte
	first, n int
}

func (n nameInput) bytes() (nameBytes, error) {
	out := nameBytes{input: n}
	if !n.static {
		var err error
		out.text, err = n.text.iterator()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
func (n *nameBytes) next() (byte, error) {
	if n.input.static {
		if n.at == len(n.input.literal) {
			return 0, io.EOF
		}
		value := n.input.literal[n.at]
		n.at++
		return value, nil
	}
	if n.first < n.n {
		value := n.encoded[n.first]
		n.first++
		return value, nil
	}
	r, err := n.text.next()
	if err != nil {
		return 0, err
	}
	n.n = utf8.EncodeRune(n.encoded[:], r)
	n.first = 1
	return n.encoded[0], nil
}

type nameBitCursor struct {
	ctx                  context.Context
	bytes                nameBytes
	nextIndex, lastIndex uint32
	last                 byte
	phase                uint8
	current              byte
	ended                bool
	endIndex             uint32
}

func (n nameInput) bitCursor() (nameBitCursor, error) {
	bytes, err := n.bytes()
	return nameBitCursor{bytes: bytes}, err
}

// at never zero-pads an unrelated subtree after end of key. past=true
// requires the caller to use the branch's representative leaf immediately.
func (c *nameBitCursor) at(target uint32) (value byte, past bool, err error) {
	if target < c.nextIndex {
		if target == c.lastIndex {
			return c.last, false, nil
		}
		return 0, false, ErrIndexInvariant
	}
	for c.nextIndex <= target {
		if c.ctx != nil {
			if err := c.ctx.Err(); err != nil {
				return 0, false, err
			}
		}
		if c.ended {
			return 0, true, nil
		}
		if c.phase == 0 {
			c.current, err = c.bytes.next()
			if err == io.EOF {
				c.ended, c.endIndex = true, c.nextIndex
				c.lastIndex, c.last = c.nextIndex, 0
				c.nextIndex++
				return 0, target > c.endIndex, nil
			}
			if err != nil {
				return 0, false, err
			}
			value, c.phase = 1, 1
		} else {
			value = c.current >> (8 - c.phase) & 1
			c.phase++
			if c.phase == 9 {
				c.phase = 0
			}
		}
		c.lastIndex, c.last = c.nextIndex, value
		c.nextIndex++
	}
	return value, false, nil
}

func (s *indexStore) compareNames(query nameInput, stored Text) (equal bool, differing uint32, queryBit byte, err error) {
	a, err := query.bytes()
	if err != nil {
		return false, 0, 0, err
	}
	b, err := (nameInput{text: stored}).bytes()
	if err != nil {
		return false, 0, 0, err
	}
	var count uint32
	for {
		if err := s.ctx.Err(); err != nil {
			return false, 0, 0, err
		}
		x, xe := a.next()
		y, ye := b.next()
		if xe != nil && xe != io.EOF {
			return false, 0, 0, xe
		}
		if ye != nil && ye != io.EOF {
			return false, 0, 0, ye
		}
		if xe == io.EOF || ye == io.EOF {
			if xe == ye {
				return true, 0, 0, nil
			}
			bit := byte(1)
			if xe == io.EOF {
				bit = 0
			}
			return false, count * 9, bit, nil
		}
		if x != y {
			leading := uint32(bits.LeadingZeros8(x ^ y))
			return false, count*9 + 1 + leading, x >> (7 - leading) & 1, nil
		}
		count++
	}
}
