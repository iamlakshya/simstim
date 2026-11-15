package egress

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type trailerEOFBody struct {
	reader     io.Reader
	eof        func()
	reachedEOF bool
	reads      int
}

func (b *trailerEOFBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) && !b.reachedEOF {
		b.reachedEOF = true
		b.eof()
	}
	return n, err
}

func (*trailerEOFBody) Close() error { return nil }

func TestHTTPResponseTrailerDeclarationsFailBeforeBodyReads(t *testing.T) {
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Host", "Connection", "Cookie", "Set-Cookie", "Forwarded", "X-Forwarded-Host"} {
		body := &trailerEOFBody{reader: strings.NewReader("not inspected"), eof: func() {}}
		response := &http.Response{ProtoMajor: 1, TransferEncoding: []string{"chunked"}, Header: make(http.Header), Trailer: http.Header{key: nil}, Body: body}
		if err := prepareHTTPResponse(response, http.MethodGet); err == nil || body.reads != 0 {
			t.Fatalf("forbidden declaration %q admitted or consumed body: reads=%d, %v", key, body.reads, err)
		}
	}
}

func TestHTTPResponseEncodedBodyCompletesFramingBeforeSafeTrailers(t *testing.T) {
	encoded := encodedBodyFixture(t, "zlib", strings.NewReader("decoded bytes"))
	response := &http.Response{ProtoMajor: 2, Header: http.Header{"Content-Encoding": []string{"deflate"}}, Trailer: http.Header{"X-Checksum": nil}}
	body := &trailerEOFBody{reader: bytes.NewReader(encoded), eof: func() { response.Trailer.Set("X-Checksum", "digest") }}
	response.Body = body
	if err := prepareHTTPResponse(response, http.MethodGet); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil || string(got) != "decoded bytes" || !body.reachedEOF || response.Trailer.Get("X-Checksum") != "digest" {
		t.Fatalf("safe trailer framing was not completed: %q eof=%v trailers=%v, %v", got, body.reachedEOF, response.Trailer, err)
	}
	if response.Header.Get("Content-Encoding") != "" || response.ContentLength != -1 {
		t.Fatal("decoded response retained encoded framing")
	}
}

func TestHTTPResponseSafeTrailersDoNotKeepFixedLengthFraming(t *testing.T) {
	response := &http.Response{ProtoMajor: 2, StatusCode: http.StatusOK, ContentLength: 4,
		Header: http.Header{"Content-Length": []string{"4"}}, Trailer: http.Header{"X-Checksum": nil}}
	response.Body = &trailerEOFBody{reader: strings.NewReader("safe"), eof: func() { response.Trailer.Set("X-Checksum", "digest") }}
	if err := prepareHTTPResponse(response, http.MethodGet); err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Length") != "" || response.ContentLength != -1 {
		t.Fatal("fixed H2 body length would suppress downstream H1 trailers")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "safe" || response.Trailer.Get("X-Checksum") != "digest" {
		t.Fatalf("safe body/trailers lost: %q, %v, %v", body, response.Trailer, err)
	}
}

func TestHTTPResponseLateForbiddenTrailerFailsAtDecodedEOF(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		payload := []byte("decoded bytes")
		response := &http.Response{ProtoMajor: 2, Header: make(http.Header), Trailer: http.Header{"X-Checksum": nil}}
		if encoded {
			payload = encodedBodyFixture(t, "zlib", bytes.NewReader(payload))
			response.Header.Set("Content-Encoding", "deflate")
		}
		body := &trailerEOFBody{reader: bytes.NewReader(payload), eof: func() { response.Trailer.Set("Authorization", "forbidden-value") }}
		response.Body = body
		if err := prepareHTTPResponse(response, http.MethodGet); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(response.Body); err == nil || !body.reachedEOF {
			t.Fatalf("late response metadata appeared complete, encoded=%v eof=%v error=%v", encoded, body.reachedEOF, err)
		}
	}
}

func TestHTTPMethodEncodedRequestValidatesFinalTrailersBeforeEOF(t *testing.T) {
	for _, forbidden := range []bool{false, true} {
		encoded := encodedBodyFixture(t, "zlib", strings.NewReader("request bytes"))
		request := &http.Request{Trailer: http.Header{"X-Checksum": nil}}
		body := &trailerEOFBody{reader: bytes.NewReader(encoded), eof: func() {
			request.Trailer.Set("X-Checksum", "digest")
			if forbidden {
				request.Trailer.Set("Authorization", "forbidden-value")
			}
		}}
		guarded := &proxyRequestBody{reader: body, request: request, declared: map[string]bool{"X-Checksum": true}, chunked: true}
		decoded, _, err := decodeHTTPBody(guarded, http.Header{"Content-Encoding": {"deflate"}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(decoded)
		if !body.reachedEOF || (err != nil) != forbidden || !forbidden && (string(got) != "request bytes" || request.Trailer.Get("X-Checksum") != "digest") {
			t.Fatalf("decoded request skipped final trailer policy: forbidden=%v body=%q eof=%v error=%v", forbidden, got, body.reachedEOF, err)
		}
	}
}
