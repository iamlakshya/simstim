package egress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestValidateRawHTTP1HeadSmuggling(t *testing.T) {
	validCases := []struct {
		name string
		raw  string
	}{
		{"simple GET", "GET /lodash HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n"},
		{"POST with Content-Length", "POST /v1/items HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 5\r\n\r\n"},
		{"POST chunked", "POST /v1/items HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n"},
		{"HTTP/1.0 no host", "GET /status HTTP/1.0\r\n\r\n"},
		{"websocket upgrade", "GET /ws HTTP/1.1\r\nHost: api.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"},
	}
	for _, tc := range validCases {
		t.Run("allow/"+tc.name, func(t *testing.T) {
			if _, err := ValidateRawHTTP1Head([]byte(tc.raw)); err != nil {
				t.Fatalf("ValidateRawHTTP1Head(%q) unexpected err: %v", tc.name, err)
			}
		})
	}

	smuggleCases := []struct {
		name string
		raw  string
	}{
		{"duplicate Host", "GET / HTTP/1.1\r\nHost: a.example\r\nHost: b.example\r\n\r\n"},
		{"missing Host HTTP/1.1", "GET / HTTP/1.1\r\nAccept: */*\r\n\r\n"},
		{"CL and TE chunked", "POST / HTTP/1.1\r\nHost: a.example\r\nContent-Length: 5\r\nTransfer-Encoding: chunked\r\n\r\n"},
		{"TE chunked and CL", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: chunked\r\nContent-Length: 5\r\n\r\n"},
		{"multiple differing CL", "POST / HTTP/1.1\r\nHost: a.example\r\nContent-Length: 5\r\nContent-Length: 10\r\n\r\n"},
		{"multiple identical CL", "POST / HTTP/1.1\r\nHost: a.example\r\nContent-Length: 5\r\nContent-Length: 5\r\n\r\n"},
		{"negative CL", "POST / HTTP/1.1\r\nHost: a.example\r\nContent-Length: -1\r\n\r\n"},
		{"plus sign CL", "POST / HTTP/1.1\r\nHost: a.example\r\nContent-Length: +5\r\n\r\n"},
		{"comma CL", "POST / HTTP/1.1\r\nHost: a.example\r\nContent-Length: 5, 5\r\n\r\n"},
		{"duplicate TE chunked", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: chunked, chunked\r\n\r\n"},
		{"gzip chunked TE", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: gzip, chunked\r\n\r\n"},
		{"obfuscated TE identity", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: identity\r\n\r\n"},
		{"TE with parameter", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: chunked;q=1\r\n\r\n"},
		{"multiple TE headers", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n"},
		{"space before colon", "POST / HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding : chunked\r\n\r\n"},
		{"obsolete line folding", "GET / HTTP/1.1\r\nHost: a.example\r\nX-Test: a\r\n b\r\n\r\n"},
		{"bare LF", "GET / HTTP/1.1\nHost: a.example\n\n"},
		{"bare CR in value", "GET / HTTP/1.1\r\nHost: a.example\r\nX-Test: foo\rbar\r\n\r\n"},
		{"NUL in header value", "GET / HTTP/1.1\r\nHost: a.example\r\nX-Test: foo\x00bar\r\n\r\n"},
		{"nested CONNECT CVE-2026-56866", "CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n"},
		{"bad HTTP version", "GET / HTTP/2.0\r\nHost: a.example\r\n\r\n"},
		{"extra space in request line", "GET  / HTTP/1.1\r\nHost: a.example\r\n\r\n"},
		{"oversized head", "GET / HTTP/1.1\r\nHost: a.example\r\nX-Big: " + strings.Repeat("a", MaxHTTP1HeadBytes) + "\r\n\r\n"},
	}
	for _, tc := range smuggleCases {
		t.Run("deny/"+tc.name, func(t *testing.T) {
			if _, err := ValidateRawHTTP1Head([]byte(tc.raw)); err == nil {
				t.Fatalf("ValidateRawHTTP1Head(%q) succeeded, want smuggling refusal", tc.name)
			}
		})
	}
}

func TestCanonicalizeRequestSmuggling(t *testing.T) {
	makeReq := func(method, host, path, rawPath, rawQuery, reqURI string, protoMajor int) *http.Request {
		return &http.Request{
			Method:     method,
			Host:       host,
			ProtoMajor: protoMajor,
			ProtoMinor: 1,
			Header:     http.Header{},
			RequestURI: reqURI,
			URL: &url.URL{
				Path:     path,
				RawPath:  rawPath,
				RawQuery: rawQuery,
			},
		}
	}

	t.Run("valid origin form and 8 KiB query", func(t *testing.T) {
		r := makeReq("GET", "registry.npmjs.org", "/lodash", "", strings.Repeat("a", MaxQueryBytes), "/lodash", 1)
		c, err := CanonicalizeRequest(r, "registry.npmjs.org", 443, "https", true)
		if err != nil {
			t.Fatalf("CanonicalizeRequest: %v", err)
		}
		if c.Path != "/lodash" || c.URL.Host != "registry.npmjs.org" {
			t.Fatalf("unexpected canon: %+v", c)
		}
	})

	t.Run("scoped npm package with allowEncodedSlash", func(t *testing.T) {
		r := makeReq("GET", "registry.npmjs.org", "/@types/node", "/@types%2fnode", "", "/@types%2fnode", 1)
		c, err := CanonicalizeRequest(r, "registry.npmjs.org", 443, "https", true)
		if err != nil {
			t.Fatalf("CanonicalizeRequest scoped npm: %v", err)
		}
		if c.WireRawPath != "/@types%2fnode" {
			t.Fatalf("WireRawPath = %q, want /@types%%2fnode", c.WireRawPath)
		}
		if _, err := CanonicalizeRequest(r, "api.example.com", 443, "https", false); err == nil {
			t.Fatal("expected %2f to be refused when allowEncodedSlash=false")
		}
	})

	denyCases := []struct {
		name       string
		req        func() *http.Request
		allowSlash bool
		wantErr    error
	}{
		{
			name:    "Host != SNI",
			req:     func() *http.Request { return makeReq("GET", "evil.example", "/pkg", "", "", "/pkg", 1) },
			wantErr: ErrHostMismatch,
		},
		{
			name:    "Host port mismatch",
			req:     func() *http.Request { return makeReq("GET", "registry.npmjs.org:8443", "/pkg", "", "", "/pkg", 1) },
			wantErr: ErrHostMismatch,
		},
		{
			name:    "HTTP/2 :authority != SNI",
			req:     func() *http.Request { return makeReq("GET", "evil.example", "/pkg", "", "", "", 2) },
			wantErr: ErrHostMismatch,
		},
		{
			name: "absolute target != SNI",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/pkg", "", "", "https://evil.example/pkg", 1)
			},
			wantErr: ErrHostMismatch,
		},
		{
			name: "absolute target scheme mismatch",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/pkg", "", "", "http://registry.npmjs.org/pkg", 1)
			},
			wantErr: ErrHostMismatch,
		},
		{
			name: "userinfo in URL",
			req: func() *http.Request {
				r := makeReq("GET", "registry.npmjs.org", "/pkg", "", "", "/pkg", 1)
				r.URL.User = url.User("user")
				return r
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "query over 8 KiB",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/pkg", "", strings.Repeat("q", MaxQueryBytes+1), "/pkg", 1)
			},
			wantErr: ErrQueryTooLong,
		},
		{
			name: "dotdot traversal",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/v1/../secret", "", "", "/v1/../secret", 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "encoded dot traversal",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/v1/../secret", "/v1/%2e%2e/secret", "", "/v1/%2e%2e/secret", 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "encoded slash blocked with path rules",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/v1/admin", "/v1%2fadmin", "", "/v1%2fadmin", 1)
			},
			allowSlash: false,
			wantErr:    ErrRequestSmuggling,
		},
		{
			name: "leading encoded slash blocked even when allowSlash",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "//admin", "/%2fadmin", "", "/%2fadmin", 1)
			},
			allowSlash: true,
			wantErr:    ErrRequestSmuggling,
		},
		{
			name: "backslash in path",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", `/v1\admin`, "", "", `/v1\admin`, 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "encoded backslash in path",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", `/v1\admin`, "/v1%5cadmin", "", "/v1%5cadmin", 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "double slash",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "//admin", "", "", "//admin", 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "encoded NUL in path",
			req: func() *http.Request {
				return makeReq("GET", "registry.npmjs.org", "/a\x00b", "/a%00b", "", "/a%00b", 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "nested CONNECT method",
			req: func() *http.Request {
				return makeReq("CONNECT", "registry.npmjs.org", "/", "", "", "/", 1)
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "HTTP/2 with Transfer-Encoding",
			req: func() *http.Request {
				r := makeReq("POST", "registry.npmjs.org", "/a", "", "", "", 2)
				r.TransferEncoding = []string{"chunked"}
				return r
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "CL+TE at http.Request level",
			req: func() *http.Request {
				r := makeReq("POST", "registry.npmjs.org", "/a", "", "", "/a", 1)
				r.TransferEncoding = []string{"chunked"}
				r.Header.Set("Content-Length", "5")
				return r
			},
			wantErr: ErrRequestSmuggling,
		},
		{
			name: "duplicate Host in Header",
			req: func() *http.Request {
				r := makeReq("GET", "registry.npmjs.org", "/a", "", "", "/a", 1)
				r.Header.Add("Host", "evil.example")
				return r
			},
			wantErr: ErrRequestSmuggling,
		},
	}

	for _, tc := range denyCases {
		t.Run("deny/"+tc.name, func(t *testing.T) {
			_, err := CanonicalizeRequest(tc.req(), "registry.npmjs.org", 443, "https", tc.allowSlash)
			if err == nil {
				t.Fatalf("expected error %v, got nil", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

func TestDecodeBoundedBodyAndWrapResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("hello-world-"), 100)

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write(payload)
	_ = gw.Close()

	got, err := DecodeBoundedBody(io.NopCloser(bytes.NewReader(gzBuf.Bytes())), "gzip", 4096)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("gzip decode: got len=%d err=%v", len(got), err)
	}

	var flBuf bytes.Buffer
	fw, _ := flate.NewWriter(&flBuf, flate.DefaultCompression)
	_, _ = fw.Write(payload)
	_ = fw.Close()

	got, err = DecodeBoundedBody(io.NopCloser(bytes.NewReader(flBuf.Bytes())), "deflate", 4096)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("deflate decode: got len=%d err=%v", len(got), err)
	}

	// Decompression bomb exceeding cap must fail with ErrBodyTooLarge.
	if _, err := DecodeBoundedBody(io.NopCloser(bytes.NewReader(gzBuf.Bytes())), "gzip", 128); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("bomb err = %v, want ErrBodyTooLarge", err)
	}

	// Unknown encoding must fail with ErrUnsupportedEncoding.
	if _, err := DecodeBoundedBody(io.NopCloser(strings.NewReader("abc")), "br", 4096); !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("br err = %v, want ErrUnsupportedEncoding", err)
	}

	// WrapDecodedResponse strips Content-Encoding and replaces Body.
	resp := &http.Response{
		Header: http.Header{"Content-Encoding": {"gzip"}},
		Body:   io.NopCloser(bytes.NewReader(gzBuf.Bytes())),
	}
	decoded, err := WrapDecodedResponse(resp, 4096)
	if err != nil || !bytes.Equal(decoded, payload) {
		t.Fatalf("WrapDecodedResponse: err=%v len=%d", err, len(decoded))
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("expected Content-Encoding cleared, got %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestValidatingHTTP1ConnKeepAliveAndChunked(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	dl := time.Now().Add(5 * time.Second)
	_ = c1.SetDeadline(dl)
	_ = c2.SetDeadline(dl)

	var rejected error
	vc := newValidatingHTTP1Conn(c2, func(err error) { rejected = err })

	stream := "GET /one HTTP/1.1\r\nHost: a.example\r\n\r\n" +
		"POST /two HTTP/1.1\r\nHost: a.example\r\nTransfer-Encoding: chunked\r\n\r\n" +
		"5\r\nhello\r\n0\r\n\r\n" +
		"POST /smuggle HTTP/1.1\r\nHost: a.example\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n"

	go func() {
		_, _ = io.WriteString(c1, stream)
		// Drain the 400 rejection response.
		_, _ = io.ReadAll(c1)
	}()

	got, _ := io.ReadAll(vc)
	if !bytes.Contains(got, []byte("GET /one")) || !bytes.Contains(got, []byte("hello")) {
		t.Fatalf("valid keep-alive requests were not forwarded cleanly: %q", got)
	}
	if bytes.Contains(got, []byte("/smuggle")) {
		t.Fatalf("smuggled 3rd request leaked through validatingHTTP1Conn: %q", got)
	}
	if !errors.Is(rejected, ErrRequestSmuggling) {
		t.Fatalf("rejected = %v, want ErrRequestSmuggling", rejected)
	}
}

func TestValidatingHTTP1ConnUpgradeHeaderDoesNotBypassSmugglingChecks(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	dl := time.Now().Add(5 * time.Second)
	_ = c1.SetDeadline(dl)
	_ = c2.SetDeadline(dl)

	var rejected error
	vc := newValidatingHTTP1Conn(c2, func(err error) { rejected = err })

	// Request 1 carries an unconfirmed Upgrade header; Request 2 on the same
	// keep-alive connection attempts CL+TE smuggling and must still be refused.
	stream := "GET /pkg HTTP/1.1\r\nHost: registry.npmjs.org\r\nUpgrade: foo\r\n\r\n" +
		"POST /smuggle HTTP/1.1\r\nHost: registry.npmjs.org\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n"

	go func() {
		_, _ = io.WriteString(c1, stream)
		_, _ = io.ReadAll(c1)
	}()

	got, _ := io.ReadAll(vc)
	if !bytes.Contains(got, []byte("GET /pkg")) {
		t.Fatalf("expected benign first request to pass, got %q", got)
	}
	if bytes.Contains(got, []byte("/smuggle")) {
		t.Fatalf("smuggled request after Upgrade header leaked through validatingHTTP1Conn: %q", got)
	}
	if !errors.Is(rejected, ErrRequestSmuggling) {
		t.Fatalf("rejected = %v, want ErrRequestSmuggling", rejected)
	}
}

func FuzzValidateRawHTTP1Head(f *testing.F) {
	f.Add([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
	f.Add([]byte("POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n"))
	f.Add([]byte("GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n"))
	f.Add([]byte("CONNECT evil:443 HTTP/1.1\r\nHost: evil:443\r\n\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		framing, err := ValidateRawHTTP1Head(data)
		if err != nil {
			return
		}
		if framing.chunked && framing.contentLength > 0 {
			t.Fatalf("accepted both chunked and contentLength > 0 for %q", data)
		}
		if bytes.HasPrefix(bytes.ToUpper(data), []byte("CONNECT ")) {
			t.Fatalf("accepted nested CONNECT for %q", data)
		}
	})
}

func FuzzCanonicalizeRequest(f *testing.F) {
	f.Add("GET", "registry.npmjs.org", "/lodash", "", "")
	f.Add("GET", "evil.example", "/lodash", "", "")
	f.Add("GET", "registry.npmjs.org", "/@types/node", "/@types%2fnode", "a=1")
	f.Add("POST", "registry.npmjs.org", "/v1/../secret", "/v1/%2e%2e/secret", "")

	f.Fuzz(func(t *testing.T, method, host, rawPath, escapedPath, rawQuery string) {
		r := &http.Request{
			Method:     method,
			Host:       host,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     http.Header{},
			URL: &url.URL{
				Path:     rawPath,
				RawPath:  escapedPath,
				RawQuery: rawQuery,
			},
		}
		c, err := CanonicalizeRequest(r, "registry.npmjs.org", 443, "https", false)
		if err != nil {
			return
		}
		if c.Host != "registry.npmjs.org" || c.Port != 443 {
			t.Fatalf("canonicalized unexpected host %s:%d", c.Host, c.Port)
		}
		if len(c.RawQuery) > MaxQueryBytes {
			t.Fatalf("accepted query over %d bytes", MaxQueryBytes)
		}
		for seg := range strings.SplitSeq(c.Path, "/") {
			if seg == "." || seg == ".." {
				t.Fatalf("accepted traversal segment %q in path %q", seg, c.Path)
			}
		}
		if strings.Contains(c.Path, "\\") {
			t.Fatalf("accepted backslash in path %q", c.Path)
		}
	})
}
