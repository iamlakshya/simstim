package egress

import (
	"errors"
	"strconv"
	"strings"
)

// A protected label has provenance from a real match. Guest text which
// happens to spell that label remains untrusted and must still be matched.
type auditPart struct {
	text      string
	protected bool
	cutAt     int
	hasCut    bool
}

var (
	errAuditTextLimit = errors.New("audit text exceeds its size limit")
	errAuditEncoding  = errors.New("invalid internal audit encoding")
)

func newAuditSecrets(pairs []secretPair) *auditSecrets {
	if len(pairs) == 0 {
		return nil
	}
	state := &auditSecrets{}
	literal := make([]string, 0, 2*len(pairs))
	encoded := make([]string, 0, 2*len(pairs))
	for _, pair := range pairs {
		literal = append(literal, pair.needle, pair.tag)
		// A larger needle cannot occur in any bounded path or field.
		if len(pair.needle) > maxScrubInputBytes {
			continue
		}
		index := len(state.labels)
		state.labels = append(state.labels, pair.tag)
		frame := "!" + strconv.Itoa(index) + ":" + strconv.Itoa(len(pair.needle)) + "!"
		encoded = append(encoded, encodeAuditBytes(pair.needle), frame)
	}
	state.literal = strings.NewReplacer(literal...)
	if len(encoded) != 0 {
		state.encoded = strings.NewReplacer(encoded...)
	}
	return state
}

// xHH fixes byte boundaries. Neither '!' nor a marker can occur in this
// alphabet, even when those literal characters occur in guest input.
func encodeAuditBytes(raw string) string {
	const hex = "0123456789abcdef"
	var out strings.Builder
	out.Grow(3 * len(raw))
	for i := range len(raw) {
		out.WriteByte('x')
		out.WriteByte(hex[raw[i]>>4])
		out.WriteByte(hex[raw[i]&15])
	}
	return out.String()
}

type auditPartBuilder struct {
	parts     []auditPart
	raw       strings.Builder
	size      int
	cutSet    bool
	rawHasCut bool
	rawCutAt  int
}

func (b *auditPartBuilder) grow(n int) bool {
	if n > maxScrubInputBytes-b.size {
		return false
	}
	b.size += n
	return true
}

func (b *auditPartBuilder) rawByte(v byte) error {
	if !b.grow(1) {
		return errAuditTextLimit
	}
	_ = b.raw.WriteByte(v) // strings.Builder.WriteByte always returns nil.
	return nil
}

func (b *auditPartBuilder) rawText(text string) error {
	if !b.grow(len(text)) {
		return errAuditTextLimit
	}
	_, _ = b.raw.WriteString(text) // strings.Builder.WriteString always returns nil.
	return nil
}

func (b *auditPartBuilder) flushRaw() {
	if b.raw.Len() != 0 || b.rawHasCut {
		b.parts = append(b.parts, auditPart{text: b.raw.String(), cutAt: b.rawCutAt, hasCut: b.rawHasCut})
		b.raw.Reset()
		b.rawHasCut, b.rawCutAt = false, 0
	}
}

func (b *auditPartBuilder) markCut() {
	if !b.cutSet {
		b.cutSet = true
		b.rawHasCut, b.rawCutAt = true, b.raw.Len()
	}
}

func (b *auditPartBuilder) label(text string) error {
	if !b.grow(len(text)) {
		return errAuditTextLimit
	}
	b.flushRaw()
	b.parts = append(b.parts, auditPart{text: text, protected: true})
	return nil
}

func (b *auditPartBuilder) part(part auditPart) error {
	if part.hasCut {
		if part.cutAt < 0 || part.cutAt > len(part.text) {
			return errAuditEncoding
		}
		if part.protected {
			if part.cutAt == 0 {
				b.markCut()
			}
			if err := b.label(part.text); err != nil {
				return err
			}
			if part.cutAt > 0 {
				b.markCut()
			}
			return nil
		}
		if err := b.rawText(part.text[:part.cutAt]); err != nil {
			return err
		}
		b.markCut()
		return b.rawText(part.text[part.cutAt:])
	}
	if part.protected {
		return b.label(part.text)
	}
	return b.rawText(part.text)
}

func (b *auditPartBuilder) take() []auditPart {
	b.flushRaw()
	return b.parts
}

// Replacer output is parsed as it is written. Only its private match frames
// create protected parts. Each frame also records consumed source bytes,
// so a query cut moves across a match without consulting its public label.
// Decoded guest bytes never re-enter this parser. The budget accounts for
// actual labels, not marker lengths.
type auditEncodedWriter struct {
	out         *auditPartBuilder
	labels      []string
	phase       byte
	hi          byte
	digits      [20]byte
	used        int
	labelIndex  int
	readingSize bool
	inputOffset int
	inputLen    int
	cutAt       int
	hasCut      bool
}

func (w *auditEncodedWriter) Write(p []byte) (int, error) {
	for i, v := range p {
		if err := w.feed(v); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

func (w *auditEncodedWriter) WriteString(p string) (int, error) {
	for i := range len(p) {
		if err := w.feed(p[i]); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

func (w *auditEncodedWriter) feed(v byte) error {
	switch w.phase {
	case 0:
		switch v {
		case 'x':
			w.phase = 1
		case '!':
			w.phase, w.used = 3, 0
		default:
			return errAuditEncoding
		}
	case 1, 2:
		n, ok := auditHex(v)
		if !ok {
			return errAuditEncoding
		}
		if w.phase == 1 {
			w.hi, w.phase = n<<4, 2
			return nil
		}
		w.phase = 0
		if w.inputOffset >= w.inputLen {
			return errAuditEncoding
		}
		w.beforeSource()
		w.inputOffset++
		return w.out.rawByte(w.hi | n)
	case 3:
		return w.tagByte(v)
	default:
		return errAuditEncoding
	}
	return nil
}

func (w *auditEncodedWriter) tagByte(v byte) error {
	if v == ':' {
		if w.readingSize {
			return errAuditEncoding
		}
		index, err := strconv.Atoi(string(w.digits[:w.used]))
		if err != nil || index < 0 || index >= len(w.labels) {
			return errAuditEncoding
		}
		w.labelIndex, w.used, w.readingSize = index, 0, true
		return nil
	}
	if v != '!' {
		if v < '0' || v > '9' || w.used == len(w.digits) {
			return errAuditEncoding
		}
		w.digits[w.used], w.used = v, w.used+1
		return nil
	}
	size, err := strconv.Atoi(string(w.digits[:w.used]))
	if !w.readingSize || err != nil || size < 1 || size > w.inputLen-w.inputOffset {
		return errAuditEncoding
	}
	w.phase, w.readingSize = 0, false
	w.beforeSource()
	if err := w.out.label(w.labels[w.labelIndex]); err != nil {
		return err
	}
	w.inputOffset += size
	if w.hasCut && w.cutAt <= w.inputOffset {
		w.out.markCut()
		w.hasCut = false
	}
	return nil
}

func (w *auditEncodedWriter) beforeSource() {
	if w.hasCut && w.cutAt <= w.inputOffset {
		w.out.markCut()
		w.hasCut = false
	}
}

func (w *auditEncodedWriter) finish() error {
	if w.phase != 0 || w.inputOffset != w.inputLen {
		return errAuditEncoding
	}
	w.beforeSource()
	if w.hasCut {
		return errAuditEncoding
	}
	return nil
}

func redactAuditParts(parts []auditPart, state *auditSecrets) ([]auditPart, bool) {
	var out auditPartBuilder
	for _, part := range parts {
		if part.protected || state == nil || state.encoded == nil {
			if err := out.part(part); err != nil {
				return nil, false
			}
			continue
		}
		if part.hasCut && (part.cutAt < 0 || part.cutAt > len(part.text)) {
			return nil, false
		}
		writer := auditEncodedWriter{
			out: &out, labels: state.labels, inputLen: len(part.text),
			cutAt: part.cutAt, hasCut: part.hasCut,
		}
		if _, err := state.encoded.WriteString(&writer, encodeAuditBytes(part.text)); err != nil || writer.finish() != nil {
			return nil, false
		}
	}
	return out.take(), true
}

func renderAuditParts(parts []auditPart) string {
	var out strings.Builder
	for _, part := range parts {
		out.WriteString(part.text)
	}
	return out.String()
}
