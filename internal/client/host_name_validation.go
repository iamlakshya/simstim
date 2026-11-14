package client

// Host labels are one to 63 ASCII bytes. The first is alphanumeric; the
// middle also allows dot, dash and underscore; the last cannot be a dot.
// Windows discards a trailing dot, so it cannot identify a distinct host.
// This is the ONE grammar for ValidHostName and streamed on_hosts entries.
type hostNameValidation struct {
	length  uint8
	invalid bool
	lastDot bool
}

func (v *hostNameValidation) Rune(r rune) {
	if v.invalid {
		return
	}
	alphanumeric := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
	if v.length == 63 || !alphanumeric && (v.length == 0 || r != '.' && r != '-' && r != '_') {
		v.invalid = true
		return
	}
	v.length++
	v.lastDot = r == '.'
}

func (v hostNameValidation) Valid() bool { return v.length != 0 && !v.invalid && !v.lastDot }
