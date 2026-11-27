package egress

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestHTTPProtocolPersistentHeaderReaderKeepsBodyAndPipelinedBytes(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("POST /first HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 4\r\n\r\nbodyGET /second HTTP/1.1\r\nHost: api.example.com\r\n\r\n"), maxHTTPHeaderBytes)
	first, raw, err := readBufferedHTTPRequest(reader)
	if err != nil || len(raw["Content-Length"]) != 2 {
		t.Fatalf("raw framing fields were folded before validation: %v, %v", raw, err)
	}
	body, err := io.ReadAll(first.Body)
	if err != nil || string(body) != "body" {
		t.Fatalf("first body=%q, %v", body, err)
	}
	second, raw, err := readBufferedHTTPRequest(reader)
	if err != nil || second.Method != "GET" || second.RequestURI != "/second" || len(raw["Host"]) != 1 {
		t.Fatalf("pipelined request lost: %+v, %v, %v", second, raw, err)
	}
}

func TestHTTPProtocolPersistentHeaderReaderRetainsThe32KiBBound(t *testing.T) {
	raw := "GET / HTTP/1.1\r\nHost: api.example.com\r\nX-Large: " + strings.Repeat("a", maxHTTPHeaderBytes) + "\r\n\r\n"
	if _, _, err := readBufferedHTTPRequest(bufio.NewReaderSize(strings.NewReader(raw), maxHTTPHeaderBytes)); err == nil {
		t.Fatal("oversized header passed the persistent reader")
	}
}
