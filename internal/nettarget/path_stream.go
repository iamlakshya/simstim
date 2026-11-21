package nettarget

import (
	"errors"
	"io"
	"unicode/utf8"
)

// PathValidatorV1 is CanonicalPath's one incremental grammar. Output is
// decoded public path DATA. A nil sink validates without retaining the path.
type PathValidatorV1 struct {
	out        io.Writer
	started    bool
	rawStarted bool
	one        [1]byte
	slash      bool
	dots       int
	escape     uint8
	high       byte
	utf        [utf8.UTFMax]byte
	used       int
	err        error
}

// NewPathValidatorV1 starts one path validator. Empty paths remain valid.
func NewPathValidatorV1(out io.Writer) *PathValidatorV1 { return &PathValidatorV1{out: out} }

// Write decodes exactly one percent-encoding layer across arbitrary chunks.
func (v *PathValidatorV1) Write(raw []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	if len(raw) != 0 && !v.rawStarted {
		v.rawStarted = true
		if raw[0] != '/' {
			v.err = errors.New("a URL path must start with a slash")
			return 0, v.err
		}
	}
	for i, b := range raw {
		if v.escape != 0 {
			n, ok := pathHexV1(b)
			if !ok {
				v.err = errors.New("invalid URL path encoding")
				return i, v.err
			}
			if v.escape == 1 {
				v.high, v.escape = n<<4, 2
				continue
			}
			b, v.escape = v.high|n, 0
		} else if b == '%' {
			v.escape = 1
			continue
		}
		if err := v.decoded(b); err != nil {
			v.err = err
			return i, err
		}
	}
	return len(raw), nil
}

func pathHexV1(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

func (v *PathValidatorV1) decoded(b byte) error {
	if !v.started {
		if b != '/' {
			return errors.New("a URL path must start with a slash")
		}
		v.started = true
	}
	if b == '\\' || b == '%' || b == '?' || b == '#' || b == '/' && v.slash {
		return errors.New("ambiguous URL path")
	}
	if b < 0x20 || b == 0x7f {
		return errors.New("a URL path cannot contain control characters")
	}
	if b == '/' {
		if v.dots == 1 || v.dots == 2 {
			return errors.New("a URL path cannot contain dot segments")
		}
		v.dots = 0
	} else if b == '.' && v.dots >= 0 {
		v.dots = min(v.dots+1, 3)
	} else {
		v.dots = -1
	}
	v.slash = b == '/'
	if v.used != 0 || b >= utf8.RuneSelf {
		v.utf[v.used] = b
		v.used++
		if utf8.FullRune(v.utf[:v.used]) {
			r, width := utf8.DecodeRune(v.utf[:v.used])
			if r == utf8.RuneError && width == 1 || width != v.used {
				return errors.New("invalid URL path encoding")
			}
			v.used = 0
		}
	}
	if v.out != nil {
		v.one[0] = b
		n, err := v.out.Write(v.one[:])
		if err != nil {
			return err
		}
		if n != 1 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Finish checks pending escapes, UTF-8 and the final path segment.
func (v *PathValidatorV1) Finish() error {
	if v.err != nil {
		return v.err
	}
	if v.escape != 0 || v.used != 0 {
		return errors.New("invalid URL path encoding")
	}
	if v.dots == 1 || v.dots == 2 {
		return errors.New("a URL path cannot contain dot segments")
	}
	return nil
}
