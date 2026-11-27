package nettarget

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// This oracle retains the earlier library-based definition independently of
// the new chunked kernel. It is not a production fallback or another parser.
func previousCanonicalPathV1(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if raw[0] != '/' {
		return "", false
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) || strings.ContainsAny(decoded, `\%?#`) || strings.Contains(decoded, "//") {
		return "", false
	}
	for _, b := range []byte(decoded) {
		if b < 0x20 || b == 0x7f {
			return "", false
		}
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return "", false
		}
	}
	return decoded, true
}

func TestCanonicalPathSharedStreamPreservesLibrarySemanticsAcrossChunks(t *testing.T) {
	for _, raw := range []string{"", "/", "/a/b", "/a%2fb", "/%E2%98%83/é", "/.../a", "/a b", "%2fa", "a", "/a//b", "/./a", "/a/..", "/%252f", "/%3F", "/%23", "/%00", "/%7f", "/%FF", "/%E2%98", "/\\", "/a%", "/a%2", "/a%2z"} {
		wanted, valid := previousCanonicalPathV1(raw)
		for _, chunk := range []int{1, 2, 3, 7} {
			var decoded strings.Builder
			stream := NewPathValidatorV1(&decoded)
			var err error
			for at := 0; at < len(raw) && err == nil; at += chunk {
				_, err = stream.Write([]byte(raw[at:min(at+chunk, len(raw))]))
			}
			finish := stream.Finish()
			if (err == nil && finish == nil) != valid || valid && decoded.String() != wanted {
				t.Fatalf("path parity raw=%q chunk=%d write=%v finish=%v", raw, chunk, err, finish)
			}
		}
	}
}
