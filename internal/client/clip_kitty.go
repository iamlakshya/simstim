package client

import (
	"encoding/base64"
	"unicode/utf8"
)

// kitty's clipboard protocol, OSC 5522, read as the pinned Ghostty commit
// reads it (src/terminal/kitty/clipboard_command.zig and the handler in
// stream_terminal.zig).
//
//   - The metadata is key=value fields that colons part. A field without
//     "=" drops the whole packet. Keys match exactly, and the last of a
//     repeated key wins. A packet whose type is not read, walias, wdata or
//     write is dropped.
//   - mime and name are strict base64 of UTF-8, at most 256 bytes. pw is
//     the same at 128 bytes, and a longer one counts as none. A bad value
//     aborts a running transfer when it comes in wdata or walias, and is
//     ignored in read and write.
//   - write starts a transfer, and drops one that runs. wdata with a MIME
//     type adds data to that type's stream. A type used again starts its
//     stream anew. wdata without a MIME type ends the transfer, and sets
//     the clipboard. Data with no transfer is ignored.
//
// The reader records each MIME type's text when its stream ends. It keeps
// those hashes until the transfer ends, and records all of them again
// then, so a commit that comes more than a day later still counts. It also
// records a stream that a new write or a bad value drops, which the
// terminal throws away: that costs a hash, and can miss nothing.

// kittyMetaReader reads metadata as it arrives, a field at a time, so a
// field of any length costs no memory beyond the limits of its key.
type kittyMetaReader struct {
	key    []byte
	inVal  bool
	val    []byte
	long   bool // the value is longer than its key can hold
	drop   bool // a field had no "="
	fields [5]kittyField
}

type kittyField struct {
	val  []byte
	long bool
}

// The keys that the reader keeps, and the longest value it keeps of each.
var kittyKeys = []struct {
	name string
	max  int
}{
	{"type", 8},
	{"loc", 8},
	{"mime", base64Len(256)},
	{"name", base64Len(256)},
	{"pw", base64Len(128)},
}

func base64Len(n int) int { return base64.StdEncoding.EncodedLen(n) }

func (m *kittyMetaReader) reset() {
	*m = kittyMetaReader{key: m.key[:0], val: m.val[:0]}
}

func (m *kittyMetaReader) byte(c byte) {
	switch {
	case c == ':':
		m.endField()
	case !m.inVal && c == '=':
		m.inVal = true
	case !m.inVal:
		if len(m.key) < 8 {
			m.key = append(m.key, c)
		} else {
			m.key = append(m.key[:0], 0) // no key is this long
		}
	default:
		if i := m.keyIndex(); i >= 0 {
			if len(m.val) < kittyKeys[i].max {
				m.val = append(m.val, c)
			} else {
				m.long = true
			}
		}
	}
}

func (m *kittyMetaReader) keyIndex() int {
	for i, k := range kittyKeys {
		if string(m.key) == k.name {
			return i
		}
	}
	return -1
}

func (m *kittyMetaReader) endField() {
	if !m.inVal {
		m.drop = true
	} else if i := m.keyIndex(); i >= 0 {
		m.fields[i] = kittyField{val: append(m.fields[i].val[:0], m.val...), long: m.long}
	}
	m.key, m.inVal, m.val, m.long = m.key[:0], false, m.val[:0], false
}

type kittyMeta struct {
	op      string
	mime    string
	primary bool
}

// meta ends the metadata, and returns it. ok is false when the terminal
// drops the packet. bad is true when a value is not valid.
func (m *kittyMetaReader) meta() (k kittyMeta, ok, bad bool) {
	m.endField()
	typ := m.fields[0]
	if m.drop || typ.long {
		return k, false, false
	}
	switch string(typ.val) {
	case "read", "walias", "wdata", "write":
	default:
		return k, false, false
	}
	k.op = string(typ.val)
	k.primary = string(m.fields[1].val) == "primary" && !m.fields[1].long
	mime, okMime := kittyValue(m.fields[2], 256)
	_, okName := kittyValue(m.fields[3], 256)
	pw := m.fields[4]
	okPw := pw.long || len(pw.val) > base64Len(128)
	if !okPw {
		_, okPw = kittyValue(pw, 1<<10) // over 128 bytes counts as none
	}
	k.mime = string(mime)
	return k, true, !okMime || !okName || !okPw
}

// kittyValue decodes a metadata value: strict base64 with padding, of
// valid UTF-8, at most maxLen bytes. An empty value is no value.
func kittyValue(f kittyField, maxLen int) ([]byte, bool) {
	if len(f.val) == 0 && !f.long {
		return nil, true
	}
	if f.long || len(f.val) > base64Len(maxLen) {
		return nil, false
	}
	d, err := base64.StdEncoding.Strict().DecodeString(string(f.val))
	if err != nil || !utf8.Valid(d) || len(d) > maxLen {
		return nil, false
	}
	return d, true
}

// kittyDone is how many finished streams a transfer keeps the hashes of.
// More make the reader lose track of the transfer.
const kittyDone = 64

// kittyTransfer is the terminal's transfer, as the reader follows it.
type kittyTransfer struct {
	active  bool
	primary bool
	unsure  bool // the reader lost track of this transfer
	mime    string
	b64     b64Stream
	text    *textHash  // the stream of the current MIME type, or nil
	reading bool       // the payload of this packet is data
	done    [][32]byte // the hashes of the streams that ended in this transfer
}

// packet acts on one packet's metadata. withData says whether a payload
// follows. lost is true when a transfer that the reader lost track of
// ended, or when a commit came for a transfer that it never saw start:
// the reader then refuses every drop for a day. While such a transfer
// runs, unsure says so.
func (k *kittyTransfer) packet(m kittyMeta, ok, bad, withData bool, record func(bool, ...[32]byte)) (lost bool) {
	k.reading = false
	switch {
	case !ok:
		return false
	case bad:
		if m.op == "wdata" || m.op == "walias" {
			return k.end(record) // the terminal aborts the transfer
		}
		return false
	}
	switch m.op {
	case "write":
		lost = k.end(record)
		k.active, k.primary = true, m.primary
	case "wdata":
		if m.mime == "" {
			// The end: the terminal sets the clipboard. With no transfer
			// that this reader saw, the terminal may hold one from before.
			if !k.active {
				return true
			}
			return k.end(record)
		}
		if !k.active {
			// The terminal ignores this, unless a transfer runs from before
			// this reader. The reader follows it, and is not sure of it.
			k.active, k.unsure = true, true
		}
		if k.text == nil || m.mime != k.mime {
			k.endStream(record)
			k.mime, k.b64, k.text = m.mime, b64Stream{}, newTextHash()
		}
		k.reading = withData
	case "walias":
		if m.mime == "" {
			lost = k.end(record) // the terminal aborts the transfer
		}
	}
	return lost
}

func (k *kittyTransfer) data(d []byte) {
	if k.reading {
		k.text.write(k.b64.write(d))
	}
}

func (k *kittyTransfer) endStream(record func(bool, ...[32]byte)) {
	if k.text == nil {
		return
	}
	k.text.write(k.b64.end())
	if s, ok := k.text.sum(); ok {
		record(k.primary, s)
		if len(k.done) < kittyDone {
			k.done = append(k.done, s)
		} else {
			k.unsure = true
		}
	}
	k.text = nil
}

// end ends the transfer, and records every stream of it again, now. lost
// is true when the reader was not sure of the transfer.
func (k *kittyTransfer) end(record func(bool, ...[32]byte)) (lost bool) {
	k.endStream(record)
	if len(k.done) > 0 {
		record(k.primary, k.done...)
	}
	lost = k.unsure
	k.active, k.primary, k.unsure, k.done = false, false, false, nil
	return lost
}

// sum is the hash of the current stream's text so far, without ending it.
func (k *kittyTransfer) sum() ([32]byte, bool) {
	if k.text == nil {
		return [32]byte{}, false
	}
	c := k.text.clone()
	b := k.b64
	c.write(b.end())
	return c.sum()
}
