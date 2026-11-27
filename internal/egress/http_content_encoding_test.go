package egress

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPAllEncodingFieldsAndCommaTokensHaveOneInterpretation(t *testing.T) {
	encoded := encodedBodyFixture(t, "gzip", strings.NewReader("authored plaintext"))
	for _, header := range []http.Header{
		{"Content-Encoding": {"identity", "gzip"}},
		{"Content-Encoding": {"identity, GZIP, identity"}},
		{"content-encoding": {"gzip"}, "Content-Encoding": {"identity"}},
		{"CONTENT-ENCODING": {"identity, identity"}, "content-encoding": {"gzip"}},
	} {
		body, changed, err := decodeHTTPBody(io.NopCloser(bytes.NewReader(encoded)), header)
		if err != nil || !changed {
			t.Fatalf("canonical decoder: %v", err)
		}
		got, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || string(got) != "authored plaintext" {
			t.Fatal("encoding aliases bypassed plaintext inspection")
		}
		for key := range header {
			if strings.EqualFold(key, "Content-Encoding") {
				t.Fatal("a residual coding header reached forwarding")
			}
		}
	}
	for _, header := range []http.Header{
		{"Content-Encoding": {"gzip", "gzip"}},
		{"Content-Encoding": {"identity, gzip, deflate"}},
		{"content-encoding": {"br"}, "Content-Encoding": {"identity"}},
		{"Content-Encoding": {""}},
		{"Content-Encoding": {", gzip"}},
		{"Content-Encoding": {"gzip,"}},
	} {
		if _, _, err := decodeHTTPBody(io.NopCloser(bytes.NewReader(encoded)), header); err == nil {
			t.Fatal("stacked, unknown or empty coding passed")
		}
	}
	header := http.Header{"content-encoding": {"IDENTITY"}}
	plain, changed, err := decodeHTTPBody(io.NopCloser(strings.NewReader("kept")), header)
	if err != nil || changed || len(header) != 0 {
		t.Fatal("identity metadata was not removed without changing body framing")
	}
	_ = plain.Close()
}
