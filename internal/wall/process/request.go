package process

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/brand"
)

// RequestVersion is the newest namespace-launcher format emitted by this helper.
const RequestVersion = 2

// MaxRequestBytes is the launcher's control-message bound.
const MaxRequestBytes = 4 << 20

// MountRequest names an inherited source descriptor and its path in the wall.
// Descriptor flags are private to the launcher. Open-file-description flags
// are never changed, since the supervisor still owns the original descriptor.
type MountRequest struct {
	FD          int    `json:"fd"`
	Guest       string `json:"guest"`
	Write       bool   `json:"write"`
	StageFD     int    `json:"stage_fd"`
	StageName   string `json:"stage_name"`
	stageFields uint8
}

// Request is the only input to the static launcher. It cannot select a program,
// its arguments, an environment, a namespace, or a security policy.
// fd3 carries this request, fd4 the link and fd5 the empty root. V2 reserves
// fd6 for its original private copy owner; mount sources start at7. V1
// compatibility starts mounts at6. The final guest receives only the link on3.
type Request struct {
	V      int            `json:"v"`
	Probe  bool           `json:"probe"`
	DNS    bool           `json:"dns"`
	Mounts []MountRequest `json:"mounts"`
}

// DecodeRequest rejects unknown and duplicate fields, non-canonical numbers,
// future versions, invalid UTF-8, and extra values. The C parser applies the
// same checks before entering a namespace.
func DecodeRequest(r io.Reader) (Request, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxRequestBytes+1))
	if err != nil {
		return Request{}, fmt.Errorf("read launcher request: %w", err)
	}
	if len(data) > MaxRequestBytes || (!utf8.Valid(data) || !validUnicodeEscapes(data)) {
		return Request{}, fmt.Errorf("launcher request exceeds its bound or has invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var req Request
	if err = decodeObject(dec, map[string]func() error{
		"v":     func() error { return decodeInteger(dec, &req.V) },
		"probe": func() error { return decodeBoolean(dec, &req.Probe) },
		"dns":   func() error { return decodeBoolean(dec, &req.DNS) },
		"mounts": func() error {
			tok, e := dec.Token()
			if e != nil || tok != json.Delim('[') {
				return fmt.Errorf("mounts must be an array")
			}
			for dec.More() {
				var m MountRequest
				e = decodeObjectOptional(dec, map[string]func() error{
					"fd": func() error { return decodeInteger(dec, &m.FD) }, "guest": func() error { return decodeString(dec, &m.Guest) }, "write": func() error { return decodeBoolean(dec, &m.Write) },
					"stage_fd":   func() error { m.stageFields |= 1; return decodeInteger(dec, &m.StageFD) },
					"stage_name": func() error { m.stageFields |= 2; return decodeString(dec, &m.StageName) },
				}, map[string]bool{"stage_fd": true, "stage_name": true})
				if e != nil {
					return e
				}
				req.Mounts = append(req.Mounts, m)
			}
			_, e = dec.Token()
			return e
		},
	}); err != nil {
		return Request{}, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return Request{}, fmt.Errorf("extra bytes after launcher request")
	}
	if err = req.Validate(); err != nil {
		return Request{}, err
	}
	for _, mount := range req.Mounts {
		if req.V == 2 && mount.stageFields != 3 {
			return Request{}, fmt.Errorf("V2 mount lacks explicit original stage roles")
		}
	}
	return req, nil
}

func decodeObject(d *json.Decoder, fields map[string]func() error) error {
	return decodeObjectOptional(d, fields, nil)
}
func decodeObjectOptional(d *json.Decoder, fields map[string]func() error, optional map[string]bool) error {
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return fmt.Errorf("launcher request needs an object")
	}
	seen := map[string]bool{}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return fmt.Errorf("read launcher field: %w", err)
		}
		key, ok := tok.(string)
		f := fields[key]
		if !ok || f == nil || seen[key] {
			return fmt.Errorf("unknown or repeated launcher field %q", key)
		}
		seen[key] = true
		if err = f(); err != nil {
			return fmt.Errorf("read launcher field %s: %w", key, err)
		}
	}
	if _, err = d.Token(); err != nil {
		return fmt.Errorf("close launcher object: %w", err)
	}
	for field := range fields {
		if !seen[field] && !optional[field] {
			return fmt.Errorf("launcher object lacks a required field")
		}
	}
	return nil
}

// Validate checks the version and descriptor and destination ownership rules.
func (r Request) Validate() error {
	if r.V < 1 || r.V > RequestVersion {
		return fmt.Errorf("unsupported launcher request version %d", r.V)
	}
	fds := map[int]bool{}
	guests := map[string]bool{}
	for _, m := range r.Mounts {
		if m.FD < 6 || r.V >= 2 && m.FD < 7 || int64(m.FD) > 2147483647 || fds[m.FD] || guests[m.Guest] || !validGuest(m.Guest) {
			return fmt.Errorf("invalid or repeated launcher mount")
		}
		if m.Write {
			for _, root := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt/" + brand.Name + "/bin"} {
				if m.Guest == root || strings.HasPrefix(m.Guest, root+"/") {
					return fmt.Errorf("writable system mount refused")
				}
			}
		}
		if r.V == 1 && (m.stageFields != 0 || m.StageFD != 0 || m.StageName != "") || r.V == 2 && m.stageFields != 0 && m.stageFields != 3 {
			return fmt.Errorf("incomplete versioned private stage")
		}
		if m.StageFD != 0 {
			if !m.Write || m.StageFD < 7 || int64(m.StageFD) > 2147483647 || fds[m.StageFD] || m.StageFD == m.FD || len(m.StageName) != 38 || !strings.HasPrefix(m.StageName, ".copy-") {
				return fmt.Errorf("invalid original factory stage")
			}
			for _, digit := range m.StageName[6:] {
				if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f') {
					return fmt.Errorf("invalid private stage identity")
				}
			}
			fds[m.StageFD] = true
		} else if m.StageName != "" {
			return fmt.Errorf("private stage name has no original descriptor")
		}
		fds[m.FD] = true
		guests[m.Guest] = true
	}
	for guest := range guests {
		for i := 1; i < len(guest); i++ {
			if guest[i] == '/' && guests[guest[:i]] {
				return fmt.Errorf("overlapping launcher mount destinations")
			}
		}
	}

	if r.Probe && (len(r.Mounts) != 0 || r.DNS) {
		return fmt.Errorf("a launcher probe cannot mount host sources")
	}
	return nil
}

func validGuest(p string) bool {
	if p == "/" || !strings.HasPrefix(p, "/") || path.Clean(p) != p || len(p) >= 4096 || !utf8.ValidString(p) {
		return false
	}
	if p == "/etc" || p == "/home" || p == "/home/agent" {
		return false
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, reserved := range []string{"/.copy-staging", "/proc", "/dev", "/run", "/root", "/sys", "/etc/passwd", "/etc/group", "/etc/hosts", "/etc/resolv.conf"} {
		if p == reserved || strings.HasPrefix(p, reserved+"/") {
			return false
		}
	}
	return true
}

// MarshalJSON keeps an empty mount array an array, including probe requests.
func (r Request) MarshalJSON() ([]byte, error) {
	type plain Request
	if r.Mounts == nil {
		r.Mounts = []MountRequest{}
	}
	if r.V == 1 {
		type legacyMount struct {
			FD    int    `json:"fd"`
			Guest string `json:"guest"`
			Write bool   `json:"write"`
		}
		mounts := make([]legacyMount, 0, len(r.Mounts))
		for _, m := range r.Mounts {
			mounts = append(mounts, legacyMount{m.FD, m.Guest, m.Write})
		}
		return json.Marshal(struct {
			V      int           `json:"v"`
			Probe  bool          `json:"probe"`
			DNS    bool          `json:"dns"`
			Mounts []legacyMount `json:"mounts"`
		}{r.V, r.Probe, r.DNS, mounts})
	}
	return json.Marshal(plain(r))
}
func decodeInteger(d *json.Decoder, out *int) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	number, ok := token.(json.Number)
	if !ok {
		return fmt.Errorf("expected integer")
	}
	n, err := strconv.Atoi(string(number))
	if err != nil || n < 0 || strconv.Itoa(n) != string(number) {
		return fmt.Errorf("expected canonical unsigned integer")
	}
	*out = n
	return nil
}
func decodeBoolean(d *json.Decoder, out *bool) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	value, ok := token.(bool)
	if !ok {
		return fmt.Errorf("expected Boolean")
	}
	*out = value
	return nil
}
func decodeString(d *json.Decoder, out *string) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	value, ok := token.(string)
	if !ok {
		return fmt.Errorf("expected string")
	}
	*out = value
	return nil
}
func validUnicodeEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for ; i < len(data) && data[i] != '"'; i++ {
			if data[i] != '\\' {
				continue
			}
			i++
			if i >= len(data) {
				return false
			}
			if data[i] != 'u' {
				continue
			}
			if i+4 >= len(data) {
				return false
			}
			value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return false
			}
			if value < 0xd800 || value > 0xdbff {
				continue
			}
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
