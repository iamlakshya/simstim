package egress

import (
	"bytes"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func physicalH2Fields(t *testing.T, encoder *hpack.Encoder, wire *bytes.Buffer, fields []hpack.HeaderField) []byte {
	t.Helper()
	wire.Reset()
	for _, field := range fields {
		if err := encoder.WriteField(field); err != nil {
			t.Fatal(err)
		}
	}
	return bytes.Clone(wire.Bytes())
}

func TestPhysicalH2CorrelationPreservesGuestDuplicatesAndDynamicDictionary(t *testing.T) {
	key, err := newPhysicalH2TokenKey()
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	encoder := hpack.NewEncoder(&encoded)
	guard := newPhysicalH2HeaderGuard()
	var internal bytes.Buffer
	for _, id := range []uint32{1, 3} {
		fields := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "api.example.test"}, {Name: strings.ToLower(physicalH2TokenHeader), Value: "guest-one"}, {Name: strings.ToLower(physicalH2TokenHeader), Value: "guest-two"}, {Name: "x-dynamic", Value: strings.Repeat("shared", 100)}}
		block := physicalH2Fields(t, encoder, &encoded, fields)
		if err := guard.begin(id); err != nil {
			t.Fatal(err)
		}
		middle := len(block) / 2
		if err := guard.fragment(id, block[:middle], false); err != nil {
			t.Fatal(err)
		}
		if err := guard.fragment(id, block[middle:], true); err != nil {
			t.Fatal(err)
		}
		framer := http2.NewFramer(&internal, nil)
		if err := framer.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: block[:middle], EndHeaders: false, EndStream: true}); err != nil {
			t.Fatal(err)
		}
		var final bytes.Buffer
		if err := http2.NewFramer(&final, nil).WriteContinuation(id, true, block[middle:]); err != nil {
			t.Fatal(err)
		}
		token, err := key.literal(id)
		if err != nil {
			t.Fatal(err)
		}
		appended, err := appendPhysicalH2TokenFinal(final.Bytes(), token, id)
		if err != nil {
			t.Fatal(err)
		}
		original := bytes.Clone(appended[:final.Len()])
		original[4] |= byte(http2.FlagContinuationEndHeaders)
		if !bytes.Equal(original, final.Bytes()) {
			t.Fatal("token insertion changed original encoded header bytes")
		}
		_, _ = internal.Write(appended)
	}
	framer := http2.NewFramer(io.Discard, bytes.NewReader(internal.Bytes()))
	framer.ReadMetaHeaders = hpack.NewDecoder(physicalH2OriginalTableLimit, nil)
		framer.MaxHeaderListSize = uint32(physicalH2OriginalHeaderLimit + physicalH2TokenOverhead)
	for _, id := range []uint32{1, 3} {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatal("official decoder lost the original dynamic dictionary", err)
		}
		headers, ok := frame.(*http2.MetaHeadersFrame)
		if !ok || headers.StreamID != id || headers.Truncated {
			t.Fatal("private continuation changed official header semantics")
		}
		request := &http.Request{Header: make(http.Header)}
		for _, field := range headers.RegularFields() {
			request.Header.Add(field.Name, field.Value)
		}
		bound, err := key.consume(request)
		if err != nil || bound != id || !reflect.DeepEqual(request.Header.Values(physicalH2TokenHeader), []string{"guest-one", "guest-two"}) || request.Header.Get("X-Dynamic") != strings.Repeat("shared", 100) {
			t.Fatal("binding consumed a guest header or confused identical requests", err)
		}
		other, err := newPhysicalH2TokenKey()
		if err != nil {
			t.Fatal(err)
		}
		value, err := key.token(id)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Add(physicalH2TokenHeader, value)
		if _, err := other.consume(request); err == nil || request.Header.Values(physicalH2TokenHeader)[2] != value {
			t.Fatal("another connection accepted or removed a guest-supplied token")
		}
	}
}

func TestPhysicalH2OriginalHeaderLimitMatchesOfficialDecoderAtExactBoundary(t *testing.T) {
	base := []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/v1/responses"}, {Name: ":authority", Value: "api.example.test"}}
	used := uint32(len("x-fill") + 32)
	for _, field := range base {
		used += field.Size()
	}
	for _, extra := range []int{0, 1} {
		fields := append(append([]hpack.HeaderField(nil), base...), hpack.HeaderField{Name: "x-fill", Value: strings.Repeat("a", maxHTTPHeaderBytes+10*32-int(used)+extra)})
		var encoded bytes.Buffer
		block := physicalH2Fields(t, hpack.NewEncoder(&encoded), &encoded, fields)
		guard := newPhysicalH2HeaderGuard()
		if err := guard.begin(1); err != nil {
			t.Fatal(err)
		}
		for at := 0; at < len(block); at++ {
			if err := guard.fragment(1, block[at:at+1], at+1 == len(block)); err != nil {
				t.Fatal("valid split HPACK changed its original size decision", err)
			}
		}
		var wire bytes.Buffer
		if err := http2.NewFramer(&wire, nil).WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block, EndHeaders: true, EndStream: true}); err != nil {
			t.Fatal(err)
		}
		framer := http2.NewFramer(io.Discard, &wire)
		framer.ReadMetaHeaders = hpack.NewDecoder(physicalH2OriginalTableLimit, nil)
		framer.MaxHeaderListSize = maxHTTPHeaderBytes + 10*32 // independent pinned standard-server limit
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		original := frame.(*http2.MetaHeadersFrame)
		if guard.truncated != original.Truncated || guard.truncated != (extra == 1) {
			t.Fatal("synthetic overhead raised or lowered the original guest header limit")
		}
	}
}
