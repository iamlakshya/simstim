package api

// HostNameValidationV1 is the shared saved-host, on_hosts and placement grammar.
type HostNameValidationV1 struct {
	length  uint8
	invalid bool
	lastDot bool
	auto    bool
}

// Rune visits one decoded character without retaining the source.
func (v *HostNameValidationV1) Rune(r rune) {
	if v.invalid {
		return
	}
	if v.length == 0 {
		v.auto = r == 'a'
	} else if v.auto {
		v.auto = v.length < 4 && r == rune("auto"[v.length])
	}
	alphanumeric := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
	if v.length == 63 || !alphanumeric && (v.length == 0 || r != '.' && r != '-' && r != '_') {
		v.invalid = true
		return
	}
	v.length++
	v.lastDot = r == '.'
}

// Valid reports a complete non-empty saved-host name.
func (v HostNameValidationV1) Valid() bool {
	return v.length != 0 && !v.invalid && !v.lastDot && !(v.auto && v.length == 4)
}
