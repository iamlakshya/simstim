package client

import "bytes"

// The parts of an OSC string, as the reader reads it.
const (
	oscNum      uint8 = iota // the number
	oscSkip                  // not a clipboard write
	osc52Sel                 // 52: the selection
	osc52Data                // 52: the base64 data
	osc5522Meta              // 5522: the metadata
	osc5522Data              // 5522: the payload
	osc1337Key               // 1337: the key
	osc1337Val               // 1337 Copy: the value, up to its ":"
	osc1337Data              // 1337 Copy: the base64 data
)

// Limits of the small parts of an OSC string that the reader keeps.
const (
	oscMaxSel  = 16
	oscMaxKey  = 32
	oscMaxName = 64
)

type oscRead struct {
	part  uint8
	num   []byte
	buf   []byte // the selection, key or value start
	val   *clipValue
	kmeta kittyMetaReader
	// The OSC 52 selection, as its letters say where the write goes. Every
	// letter but "p" counts for the clipboard: the pinned terminal sends
	// unknown ones there, and xterm may send "s" there. "p" and "s" count
	// for a selection.
	toBoard, toSel bool
	longSel        bool // the selection is longer than oscMaxSel
}

// oscHead reads one byte of the start of an OSC string.
func (r *clipReader) oscHead(c byte) {
	o := &r.osc
	switch o.part {
	case oscNum:
		r.oscNumByte(c)
	case osc52Sel:
		// The selection runs to its ";", and never into the data.
		if c == ';' {
			r.start52()
			return
		}
		o.toBoard = o.toBoard || c != 'p'
		o.toSel = o.toSel || c == 'p' || c == 's'
		if len(o.buf) < oscMaxSel {
			o.buf = append(o.buf, c)
		} else {
			o.longSel = true
		}
	case osc5522Meta:
		if c == ';' {
			r.kittyPacket(true)
			o.part = osc5522Data
			return
		}
		o.kmeta.byte(c)
	case osc1337Key:
		if c == '=' {
			if bytes.EqualFold(o.buf, []byte("Copy")) {
				o.part, o.buf = osc1337Val, o.buf[:0]
				return
			}
			if bytes.EqualFold(o.buf, []byte("CopyToClipboard")) {
				r.startCopy()
			}
			o.part = oscSkip
			return
		}
		if o.buf = append(o.buf, c); len(o.buf) > oscMaxKey {
			o.part = oscSkip
		}
	case osc1337Val:
		r.oscValByte(c)
	}
}

func (r *clipReader) oscNumByte(c byte) {
	o := &r.osc
	switch {
	case c == '0' && len(o.num) == 0:
		// A leading zero: xterm reads "052" as 52.
	case c >= '0' && c <= '9' && len(o.num) < 6:
		o.num = append(o.num, c)
	case c == ';':
		o.buf = o.buf[:0]
		switch string(o.num) {
		case "52":
			o.part = osc52Sel
		case "5522":
			o.part = osc5522Meta
		case "1337":
			o.part = osc1337Key
		default:
			o.part = oscSkip
		}
	default:
		o.part = oscSkip
	}
}

// oscValByte reads the value of iTerm2's Copy up to its ":". Without a ":"
// in its first bytes, the whole value is data.
func (r *clipReader) oscValByte(c byte) {
	o := &r.osc
	if c == ':' {
		o.val = newClipValue()
		o.part = osc1337Data
		return
	}
	if o.buf = append(o.buf, c); len(o.buf) > oscMaxName {
		o.val = newClipValue()
		o.part = osc1337Data
		o.val.write(o.buf)
	}
}

// kittyPacket acts on a kitty packet. The reader lost track of a write
// when the transfer becomes one that it is not sure of, and again when
// such a transfer ends.
func (r *clipReader) kittyPacket(withData bool) {
	m, ok, bad := r.osc.kmeta.meta()
	k := &r.term.kitty
	was := k.active && k.unsure
	if k.packet(m, ok, bad, withData, r.rec) || !was && k.active && k.unsure {
		r.lost()
	}
}

// start52 starts the data of an OSC 52 write. A selection longer than
// oscMaxSel is one that the reader cannot be sure of, so it lost track of
// the write, as well as recording it.
func (r *clipReader) start52() {
	o := &r.osc
	if o.longSel {
		r.lost()
	}
	if len(o.buf) == 0 {
		o.toBoard = true // no selection: the clipboard
	}
	o.part = osc52Data
	o.val = newClipValue()
	sel := string(o.buf)
	if sel == "" {
		sel = "c"
	}
	run := r.term.running[sel]
	if run == nil && len(r.term.running) < oscMaxSel {
		run = newTextHash()
		r.term.running[sel] = run
	}
	o.val.tee = run
}

// oscData reads data of a clipboard write.
func (r *clipReader) oscData(d []byte) {
	switch r.osc.part {
	case osc52Data, osc1337Data:
		r.osc.val.write(d)
	case osc5522Data:
		r.term.kitty.data(d)
	}
}

// endOSC acts on the OSC string that just ended.
func (r *clipReader) endOSC() {
	o := &r.osc
	switch o.part {
	case osc52Sel:
		// "52;data", with no ";" after the selection: some terminal may
		// take it as data. A long one the reader cannot read whole.
		if o.longSel {
			r.lost()
			break
		}
		data := append([]byte(nil), o.buf...)
		o.buf, o.toBoard, o.toSel = o.buf[:0], true, false
		r.start52()
		r.oscData(data)
		r.endValue()
	case osc52Data, osc1337Data:
		r.endValue()
	case osc1337Val:
		o.val = newClipValue()
		o.val.write(o.buf)
		r.endValue()
	case osc5522Meta:
		r.kittyPacket(false)
	case osc1337Key:
		switch {
		case bytes.EqualFold(o.buf, []byte("CopyToClipboard")):
			r.startCopy()
		case bytes.EqualFold(o.buf, []byte("EndCopy")):
			r.endCopy()
		}
	}
	o.val = nil
	if cap(o.buf) > 4096 {
		o.buf = nil
	}
}

// endValue records an OSC 52 or iTerm2 Copy write, to each place that its
// selection may name. The running text counts only when the write added
// some.
func (r *clipReader) endValue() {
	o := &r.osc
	v := o.val
	board, sel := true, false
	if o.part == osc52Data {
		board, sel = o.toBoard, o.toSel
	}
	sums := v.end()
	if v.tee != nil && v.ts.keys > 0 {
		if s, ok := v.tee.sum(); ok {
			sums = append(sums, s)
		}
	}
	if board {
		r.rec(false, sums...)
	}
	if sel {
		r.rec(true, sums...)
	}
}
