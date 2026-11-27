package egress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
	"testing"
)

func encodedBodyFixture(t *testing.T, encoding string, input io.Reader) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&buffer)
	case "zlib":
		writer = zlib.NewWriter(&buffer)
	case "raw":
		var err error
		writer, err = flate.NewWriter(&buffer, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown fixture encoding")
	}
	if _, err := io.Copy(writer, input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestHTTPCompressionCanonicalBodyAndUnsupportedEncodings(t *testing.T) {
	for _, encoding := range []string{"gzip", "zlib", "raw"} {
		t.Run(encoding, func(t *testing.T) {
			encoded := encodedBodyFixture(t, encoding, strings.NewReader("canonical request body"))
			wire := encoding
			if wire != "gzip" {
				wire = "deflate"
			}
			body, changed, err := decodeHTTPBody(io.NopCloser(bytes.NewReader(encoded)), http.Header{"Content-Encoding": {wire}})
			if err != nil || !changed {
				t.Fatalf("decode setup: changed=%v, %v", changed, err)
			}
			defer body.Close()
			got, err := io.ReadAll(body)
			if err != nil || string(got) != "canonical request body" {
				t.Fatalf("canonical body = %q, %v", got, err)
			}
		})
	}
	for _, encoding := range []string{"br", "gzip, gzip", "gzip, identity", "compress"} {
		if _, _, err := decodeHTTPBody(io.NopCloser(strings.NewReader("not decoded")), http.Header{"Content-Encoding": {encoding}}); err == nil {
			t.Fatalf("unsupported encoding accepted: %q", encoding)
		}
	}
}

type repeatBodyByte struct{}

func (repeatBodyByte) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestHTTPCompressionEnforcesTheActual64MiBLimit(t *testing.T) {
	for _, size := range []int64{maxDecodedHTTPBody, maxDecodedHTTPBody + 1} {
		encoded := encodedBodyFixture(t, "gzip", io.LimitReader(repeatBodyByte{}, size))
		body, _, err := decodeHTTPBody(io.NopCloser(bytes.NewReader(encoded)), http.Header{"Content-Encoding": {"gzip"}})
		if err != nil {
			t.Fatal(err)
		}
		n, readErr := io.Copy(io.Discard, body)
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
		if n != maxDecodedHTTPBody || (readErr != nil) != (size > maxDecodedHTTPBody) {
			t.Fatalf("decoded %d bytes from %d, error=%v", n, size, readErr)
		}
	}
}
