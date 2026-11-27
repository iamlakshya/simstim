package egress

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

type wsOneByteReader struct{ io.Reader }

func (r wsOneByteReader) Read(output []byte) (int, error) {
	if len(output) > 1 {
		output = output[:1]
	}
	return r.Reader.Read(output)
}

// RFC 6455 section 5.7's independent masked "Hello" example. This does not
// construct expected bytes using the encoder being tested.
func TestBoundWebSocketRFCMaskedHelloAndSplitMaskOffsets(t *testing.T) {
	wire := []byte{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58}
	for split := 0; split <= len(wire); split++ {
		input := io.MultiReader(bytes.NewReader(wire[:split]), wsOneByteReader{bytes.NewReader(wire[split:])})
		frame, err := readWebSocketFrame(input, true)
		if err != nil || frame.opcode != 1 || !frame.final {
			t.Fatalf("split %d header: %v", split, err)
		}
		payload, err := io.ReadAll(&webSocketFramePayload{frame: frame, input: input})
		if err != nil || string(payload) != "Hello" || frame.position != 5 || frame.remaining != 0 {
			t.Fatalf("split %d mask offsets or payload changed", split)
		}
	}
	payload := []byte("Hello")
	encoded, err := encodeWebSocketFrame(1, true, true, payload, bytes.NewReader([]byte{0x37, 0xfa, 0x21, 0x3d}))
	if err != nil || !bytes.Equal(encoded, wire) || string(payload) != "Hello" {
		t.Fatal("RFC output mask or source ownership changed")
	}
}

func TestBoundWebSocketRejectsInvalidFrameHeadersBeforePayload(t *testing.T) {
	cases := map[string][]byte{
		"server mask":          {0x81, 0x80, 0, 0, 0, 0},
		"reserved bit one":     {0xc1, 0},
		"reserved bit two":     {0xa1, 0},
		"reserved bit three":   {0x91, 0},
		"reserved opcode":      {0x83, 0},
		"fragmented ping":      {0x09, 0},
		"long control":         {0x89, 126, 0, 126},
		"nonminimal sixteen":   {0x82, 126, 0, 125},
		"nonminimal sixtyfour": {0x82, 127, 0, 0, 0, 0, 0, 0, 0xff, 0xff},
		"negative length":      {0x82, 127, 0x80, 0, 0, 0, 0, 1, 0, 0},
		"short header":         {0x81},
		"short extended":       {0x82, 126, 0},
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readWebSocketFrame(bytes.NewReader(wire), false); err == nil {
				t.Fatal("invalid frame header passed")
			}
		})
	}
	if _, err := readWebSocketFrame(bytes.NewReader([]byte{0x81, 0}), true); err == nil {
		t.Fatal("unmasked client frame passed")
	}
	if _, err := readWebSocketFrame(bytes.NewReader([]byte{0x81, 0x80, 1, 2}), true); err == nil {
		t.Fatal("short client mask passed")
	}
}

func TestBoundWebSocketOutputFramesAreBoundedAndFreshlyMasked(t *testing.T) {
	mask := bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	first, err := encodeWebSocketFrame(2, false, true, []byte{0, 1, 2, 3, 4}, mask)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encodeWebSocketFrame(0, true, true, []byte{0, 1, 2, 3, 4}, mask)
	if err != nil || bytes.Equal(first[2:6], second[2:6]) {
		t.Fatal("new client frame reused its previous masking key")
	}
	if _, err := encodeWebSocketFrame(1, true, true, []byte("test"), strings.NewReader("x")); err == nil {
		t.Fatal("failed mask generation emitted a frame")
	}
	for _, size := range []int{webSocketChunk + 1, webSocketChunk + 2} {
		if _, err := encodeWebSocketFrame(2, false, false, make([]byte, size), nil); err == nil {
			t.Fatal("output encoder accepted more than one bounded chunk")
		}
	}
}

func TestBoundWebSocketIncrementalUTF8AndInvalidText(t *testing.T) {
	const valid = "a£€😀z"
	for split := 0; split <= len(valid); split++ {
		input := io.MultiReader(strings.NewReader(valid[:split]), wsOneByteReader{strings.NewReader(valid[split:])})
		reader := newWebSocketTextReader(input)
		got, err := io.ReadAll(reader)
		reader.clear()
		if err != nil || string(got) != valid {
			t.Fatalf("valid UTF-8 split %d changed", split)
		}
	}
	for _, bad := range [][]byte{{0xc0, 0x80}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80}, {0xe2, 0x82}, {0x80}, {0xff}} {
		reader := newWebSocketTextReader(wsOneByteReader{bytes.NewReader(bad)})
		got, err := io.ReadAll(reader)
		reader.clear()
		var failure webSocketFailure
		if !errors.As(err, &failure) || failure.code != 1007 || len(got) != 0 {
			t.Fatal("invalid UTF-8 was released")
		}
	}
}

func TestBoundWebSocketCloseCodesAndUTF8Reasons(t *testing.T) {
	for _, code := range []uint16{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1011, 1012, 1013, 1014, 3000, 4999} {
		payload := []byte{byte(code >> 8), byte(code), 'o', 'k'}
		if err := validateWebSocketClose(payload, false); err != nil {
			t.Fatalf("valid close code %d refused", code)
		}
	}
	if err := validateWebSocketClose([]byte{3, 242}, true); err != nil { // client-only 1010
		t.Fatal("client extension failure close refused")
	}
	for _, code := range []uint16{999, 1004, 1005, 1006, 1010, 1015, 1016, 2999, 5000} {
		payload := []byte{byte(code >> 8), byte(code)}
		if err := validateWebSocketClose(payload, false); err == nil {
			t.Fatalf("invalid server close code %d passed", code)
		}
	}
	for _, payload := range [][]byte{{3}, {3, 232, 0xff}} {
		if err := validateWebSocketClose(payload, false); err == nil {
			t.Fatal("invalid close payload passed")
		}
	}
}

func TestBoundWebSocketMessageLimitUsesDeclaredLogicalTotal(t *testing.T) {
	var next [10]byte
	next[0], next[1] = 0x80, 127
	binary.BigEndian.PutUint64(next[2:], maxDecodedHTTPBody)
	message := &webSocketMessageReader{
		direction: &webSocketDirection{input: bytes.NewReader(next[:])},
		frame:     &webSocketFrame{opcode: 2, final: false}, declared: 1,
	}
	var probe [1]byte
	_, err := message.Read(probe[:])
	var failure webSocketFailure
	if !errors.As(err, &failure) || failure.code != 1009 {
		t.Fatal("continuation declaration bypassed the logical 64 MiB limit")
	}
	for _, opcode := range []byte{0, 1, 2} {
		wire := []byte{0x80 | opcode, 0}
		message := &webSocketMessageReader{direction: &webSocketDirection{input: bytes.NewReader(wire)}, frame: &webSocketFrame{opcode: 2, final: false}}
		_, err := message.Read(probe[:])
		if (err == io.EOF) != (opcode == 0) {
			t.Fatal("invalid fragmented message sequence passed")
		}
	}
}

func TestBoundWebSocketExtendedLengthsPreservePayloadAndMinimalWire(t *testing.T) {
	for _, size := range []int{125, 126, 65535, 65536} {
		payload := bytes.Repeat([]byte{0x7f, 0x80, 0xff, 0}, (size+3)/4)[:size]
		wire := wsTestWire(2, true, false, payload)
		headerBytes := 2
		if size >= 126 {
			headerBytes = 4
		}
		if size >= 65536 {
			headerBytes = 10
		}
		for split := 0; split <= headerBytes; split++ {
			input := io.MultiReader(bytes.NewReader(wire[:split]), bytes.NewReader(wire[split:]))
			frame, err := readWebSocketFrame(input, false)
			if err != nil || frame.remaining != uint64(size) {
				t.Fatal("valid extended length changed")
			}
			got, err := io.ReadAll(&webSocketFramePayload{frame: frame, input: input})
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("extended-length payload was truncated or changed")
			}
		}
	}
}

func TestBoundWebSocketInitialMessageLimitRefusesBeforeBodyRead(t *testing.T) {
	var header [10]byte
	header[0], header[1] = 0x82, 127
	binary.BigEndian.PutUint64(header[2:], maxDecodedHTTPBody+1)
	direction := &webSocketDirection{input: bytes.NewReader(header[:])}
	err := direction.run()
	var failure webSocketFailure
	if !errors.As(err, &failure) || failure.code != 1009 {
		t.Fatal("oversize declared initial message reached a filter or body read")
	}
	message := &webSocketMessageReader{frame: &webSocketFrame{final: true}, declared: maxDecodedHTTPBody}
	var probe [1]byte
	if _, err := message.Read(probe[:]); err != io.EOF {
		t.Fatal("exact 64 MiB logical count was refused")
	}
}
