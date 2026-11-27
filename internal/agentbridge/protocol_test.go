package agentbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func validScope() Scope {
	return Scope{Session: "ab2cdef3", Lease: "0123456789abcdef0123456789abcdef"}
}

func messageBytes(body string) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(body))), body...)
}

func TestScopeRequiresCanonicalSessionAndLeaseEncoding(t *testing.T) {
	good := validScope()
	for _, session := range []string{"a", good.Session, strings.Repeat("z", 32)} {
		s := good
		s.Session = session
		if err := s.Check(); err != nil {
			t.Fatalf("canonical session %q: %v", session, err)
		}
	}
	for _, session := range []string{"", "AB2CDEF3", "ab2cdef0", "ab2cdef1", "ab2cdef8", "../abc2", "abc2/def", "abc2 def", "abc2\n", "ébc2", strings.Repeat("a", 33)} {
		s := good
		s.Session = session
		if err := s.Check(); err == nil {
			t.Fatalf("accepted noncanonical session %q", session)
		}
	}
	for _, lease := range []string{"", good.Lease[:31], good.Lease + "0", strings.ToUpper(good.Lease), strings.Repeat("g", 32), strings.Repeat("0", 31) + "\n"} {
		s := good
		s.Lease = lease
		if err := s.Check(); err == nil {
			t.Fatalf("accepted noncanonical lease %q", lease)
		}
	}
	seen := map[string]bool{}
	for range 32 {
		lease, err := NewLease()
		if err != nil {
			t.Fatal(err)
		}
		s := good
		s.Lease = lease
		if err := s.Check(); err != nil || seen[lease] {
			t.Fatalf("invalid or repeated fresh lease: %v", err)
		}
		seen[lease] = true
	}
}

func TestHeaderRejectsAmbiguousUnknownOrInvalidScope(t *testing.T) {
	const scope = `"session":"ab2cdef3","lease":"0123456789abcdef0123456789abcdef"`
	for name, body := range map[string]string{
		"unknown":               `{` + scope + `,"kind":"ctl","extra":true}`,
		"duplicate":             `{` + scope + `,"kind":"ctl","session":"other2"}`,
		"escaped alias":         `{` + scope + `,"kind":"ctl","sess\u0069on":"other2"}`,
		"case alias":            `{` + scope + `,"kind":"ctl","Session":"other2"}`,
		"Unicode alias":         `{` + scope + `,"kind":"ctl","ſession":"other2"}`,
		"escaped Unicode alias": `{` + scope + `,"kind":"ctl","\u017fession":"other2"}`,
		"Unicode single field":  `{"ſession":"ab2cdef3","lease":"0123456789abcdef0123456789abcdef","kind":"ctl"}`,
		"Unicode lease alias":   `{` + scope + `,"kind":"ctl","leaſe":"fedcba9876543210fedcba9876543210"}`,
		"escaped Unicode lease": `{` + scope + `,"kind":"ctl","lea\u017fe":"fedcba9876543210fedcba9876543210"}`,
		"nested scope":          `{"scope":{` + scope + `},"kind":"ctl"}`,
		"missing scope":         `{"kind":"ctl"}`,
		"bad session":           `{"session":"ABC2","lease":"0123456789abcdef0123456789abcdef","kind":"ctl"}`,
		"bad lease":             `{"session":"abc2","lease":"short","kind":"ctl"}`,
		"null":                  `null`,
		"trailing":              `{` + scope + `,"kind":"ctl"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			var h Header
			if err := json.Unmarshal([]byte(body), &h); err == nil {
				t.Fatalf("accepted ambiguous header: %s", body)
			}
		})
	}
	var good Header
	if err := json.Unmarshal([]byte(`{`+scope+`,"kind":"ctl"}`), &good); err != nil || good.Scope != validScope() || good.Kind != "ctl" {
		t.Fatalf("canonical header = %+v, %v", good, err)
	}
}

func TestControlMessageRejectsDuplicatesCaseAliasesUnknownAndTrailingData(t *testing.T) {
	const scope = `"session":"ab2cdef3","lease":"0123456789abcdef0123456789abcdef"`
	for name, body := range map[string]string{
		"duplicate action":          `{` + scope + `,"action":"bind","action":"activate","seq":1}`,
		"case action":               `{` + scope + `,"action":"bind","Action":"activate","seq":1}`,
		"escaped alias":             `{` + scope + `,"action":"bind","act\u0069on":"activate","seq":1}`,
		"Unicode seq alias":         `{` + scope + `,"action":"bind","seq":1,"ſeq":2}`,
		"escaped Unicode seq alias": `{` + scope + `,"action":"bind","seq":1,"\u017feq":2}`,
		"unknown":                   `{` + scope + `,"action":"bind","seq":1,"extra":false}`,
		"nested scope":              `{"scope":{` + scope + `},"action":"bind","seq":1}`,
		"negative seq":              `{` + scope + `,"action":"bind","seq":-1}`,
		"fractional seq":            `{` + scope + `,"action":"bind","seq":1.5}`,
		"overflow seq":              `{` + scope + `,"action":"bind","seq":18446744073709551616}`,
		"second object":             `{` + scope + `,"action":"bind","seq":1} {}`,
		"trailing garbage":          `{` + scope + `,"action":"bind","seq":1} garbage`,
	} {
		t.Run(name, func(t *testing.T) {
			var request Request
			if err := ReadMessage(bytes.NewReader(messageBytes(body)), &request); err == nil {
				t.Fatalf("accepted ambiguous control request: %s", body)
			}
		})
	}
	for _, body := range []string{
		`{` + scope + `,"action":"bind","seq":1,"ok":false,"error":{"code":"error","message":"one","message":"two"}}`,
		`{` + scope + `,"action":"bind","seq":1,"ok":false,"error":{"code":"error","Message":"case alias"}}`,
		`{` + scope + `,"action":"bind","seq":1,"ok":false,"error":{"code":"error","message":"one","meſsage":"two"}}`,
		`{` + scope + `,"action":"bind","seq":1,"ok":false,"error":{"code":"error","message":"one","me\u017fsage":"two"}}`,
		`{` + scope + `,"action":"bind","seq":1,"ok":false,"error":{"code":"error","meſſage":"single alias"}}`,
		`{` + scope + `,"action":"bind","seq":1,"ok":false,"error":{"code":"error","message":"message","extra":true}}`,
	} {
		var reply Reply
		if err := ReadMessage(bytes.NewReader(messageBytes(body)), &reply); err == nil {
			t.Fatalf("accepted ambiguous nested error: %s", body)
		}
	}
}

type fragmentReader struct{ *bytes.Reader }

func (r fragmentReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestControlMessagesSurviveFragmentationWithoutConsumingTheNext(t *testing.T) {
	first := Request{Scope: validScope(), Action: "bind", Seq: 1}
	second := Request{Scope: validScope(), Action: "activate", Seq: 2}
	var buffer bytes.Buffer
	if err := WriteMessage(&buffer, first); err != nil {
		t.Fatal(err)
	}
	firstSize := buffer.Len()
	if err := WriteMessage(&buffer, second); err != nil {
		t.Fatal(err)
	}
	reader := fragmentReader{bytes.NewReader(buffer.Bytes())}
	for i, want := range []Request{first, second} {
		var got Request
		if err := ReadMessage(reader, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("message %d = %+v, %v", i, got, err)
		}
		if i == 0 && reader.Len() != buffer.Len()-firstSize {
			t.Fatal("reading the first message consumed bytes from the second")
		}
	}
	if reader.Len() != 0 {
		t.Fatal("message framing left unconsumed bytes")
	}
}

func TestControlRepliesRetainTheirExactScopeAndError(t *testing.T) {
	for _, want := range []Reply{
		{Scope: validScope(), Action: "activate", Seq: 2, OK: true},
		{Scope: validScope(), Action: "bind", Seq: 1, Error: &proto.Error{
			Code: "ssh_agent_refused", Message: "the endpoint is refused", Hint: "Attach again.",
		}},
	} {
		var encoded bytes.Buffer
		if err := WriteMessage(&encoded, want); err != nil {
			t.Fatal(err)
		}
		var got Reply
		if err := ReadMessage(fragmentReader{bytes.NewReader(encoded.Bytes())}, &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("reply scope or error changed: %+v, %v", got, err)
		}
	}
}

type lengthProbe struct {
	length uint32
	reads  int
}

func (r *lengthProbe) Read(p []byte) (int, error) {
	r.reads++
	if r.reads != 1 {
		return 0, errors.New("unexpected control body read")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], r.length)
	return copy(p, header[:]), nil
}

func TestControlMessageBoundsAreCheckedBeforeReadingTheBody(t *testing.T) {
	for _, length := range []uint32{0, 513, ^uint32(0)} {
		reader := &lengthProbe{length: length}
		var got Request
		if err := ReadMessage(reader, &got); err == nil || reader.reads != 1 {
			t.Fatalf("length %d: err=%v, reads=%d", length, err, reader.reads)
		}
	}
	var boundary bytes.Buffer
	want := strings.Repeat("x", 510) // JSON quotes make exactly 512 bytes.
	if err := WriteMessage(&boundary, want); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(boundary.Bytes()[:4]); got != 512 {
		t.Fatalf("boundary body size=%d", got)
	}
	var got string
	if err := ReadMessage(&boundary, &got); err != nil || got != want {
		t.Fatalf("maximum-size control = %q, %v", got, err)
	}
	for _, value := range []any{strings.Repeat("x", 511), func() {}} {
		var untouched bytes.Buffer
		if err := WriteMessage(&untouched, value); err == nil || untouched.Len() != 0 {
			t.Fatalf("invalid control value was partly written: err=%v, bytes=%d", err, untouched.Len())
		}
	}
	for _, body := range [][]byte{{1, 0, 0}, append([]byte{5, 0, 0, 0}, []byte("xx")...)} {
		var value any
		if err := ReadMessage(bytes.NewReader(body), &value); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated message = %v", err)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func TestWriteMessageUsesOneWholeWriteAndRejectsShortSuccess(t *testing.T) {
	failed := errors.New("controlled control write failure")
	for _, outcome := range []struct {
		name   string
		adjust int
		zero   bool
		err    error
	}{
		{"complete", 0, false, nil},
		{"short", -1, false, nil},
		{"zero", 0, true, nil},
		{"oversized count", 1, false, nil},
		{"error", 0, false, failed},
	} {
		calls := 0
		var bytesSeen []byte
		err := WriteMessage(writerFunc(func(p []byte) (int, error) {
			calls++
			bytesSeen = append([]byte(nil), p...)
			if outcome.zero {
				return 0, outcome.err
			}
			return len(p) + outcome.adjust, outcome.err
		}), Request{Scope: validScope(), Action: "bind", Seq: 1})
		want := outcome.err
		if want == nil && (outcome.adjust != 0 || outcome.zero) {
			want = io.ErrShortWrite
		}
		if calls != 1 || !errors.Is(err, want) {
			t.Fatalf("%s: calls=%d err=%v want=%v", outcome.name, calls, err, want)
		}
		if len(bytesSeen) <= 4 || int(binary.LittleEndian.Uint32(bytesSeen[:4])) != len(bytesSeen)-4 {
			t.Fatal("the single write did not hold the whole length-prefixed message")
		}
	}
}

func TestControlNestingAndLinkBoundsRemainFixed(t *testing.T) {
	for depth, wantError := range map[int]bool{16: false, 17: true} {
		body := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		var value any
		if err := ReadMessage(bytes.NewReader(messageBytes(body)), &value); (err != nil) != wantError {
			t.Fatalf("nesting depth %d: %v", depth, err)
		}
	}
	o := Options()
	if o.Window != 65536 || o.MaxGuestStreams != 16 || o.MaxSupervisorStreams != 1 || o.MaxHeader != 256 || o.HelloTimeout != 10*time.Second {
		t.Fatalf("agent bridge resource bounds changed: %+v", o)
	}
	if MaxMessage != 512 || Heartbeat != 5*time.Second || LeaseTimeout != 15*time.Second || HandshakeTimeout != 10*time.Second {
		t.Fatal("agent bridge message or lifetime bounds changed")
	}
	if len(o.Opener) != 2 || o.Opener[link.KindCtl] != link.Supervisor || o.Opener[link.KindAgent] != link.Guest {
		t.Fatalf("unexpected stream admission table: %v", o.Opener)
	}
	o.Opener[link.KindAgent] = link.Supervisor
	if Options().Opener[link.KindAgent] != link.Guest {
		t.Fatal("one caller changed the shared admission policy")
	}
}
