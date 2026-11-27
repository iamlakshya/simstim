package egress

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"net/http"
	"strconv"
	"strings"
)

// This consumes only successful native plaintext write prefixes. HTTP body
// bytes are separated from headers, chunk sizes, padding and trailers before
// they reach the original turn. It owns no goroutine, permit or connection.
type physicalHTTP1Body struct {
	request   bool
	method    string
	header    []byte
	line      []byte
	remaining int64
	state     uint8
	chunked   bool
	failed    bool
	observe   func([]byte)
}

const (
	physicalH1Headers uint8 = iota
	physicalH1Body
	physicalH1ChunkLine
	physicalH1ChunkData
	physicalH1ChunkCR
	physicalH1ChunkLF
	physicalH1Trailers
	physicalH1Done
)

func (b *physicalHTTP1Body) feed(payload []byte) error {
	if b.failed {
		return errPhysicalH2Binding
	}
	err := b.consume(payload)
	b.failed = err != nil
	return err
}

func (b *physicalHTTP1Body) consume(payload []byte) error {
	for len(payload) != 0 {
		switch b.state {
		case physicalH1Headers:
			b.header = append(b.header, payload[0])
			payload = payload[1:]
			if len(b.header) > maxHTTPHeaderBytes+4096 {
				return errPhysicalH2Binding
			}
			if !bytes.HasSuffix(b.header, []byte("\r\n\r\n")) {
				continue
			}
			reader := bufio.NewReader(bytes.NewReader(b.header))
			if b.request {
				request, err := http.ReadRequest(reader)
				if err != nil {
					return errPhysicalH2Binding
				}
				b.remaining, b.chunked = request.ContentLength, len(request.TransferEncoding) != 0
			} else {
				response, err := http.ReadResponse(reader, &http.Request{Method: b.method})
				if err != nil {
					return errPhysicalH2Binding
				}
				if response.StatusCode >= 100 && response.StatusCode < 200 && response.StatusCode != http.StatusSwitchingProtocols {
					b.header = b.header[:0]
					continue
				}
				b.remaining, b.chunked = response.ContentLength, len(response.TransferEncoding) != 0
				if response.Body == http.NoBody {
					b.remaining, b.chunked = 0, false
				}
			}
			clear(b.header)
			b.header = nil
			b.state = physicalH1Body
			if b.chunked {
				b.state = physicalH1ChunkLine
			} else if b.remaining == 0 {
				b.state = physicalH1Done
			}
		case physicalH1Body, physicalH1ChunkData:
			count := len(payload)
			if b.remaining >= 0 {
				count = min(count, int(b.remaining))
			}
			if count > 0 {
				b.observe(payload[:count])
				payload = payload[count:]
				if b.remaining >= 0 {
					b.remaining -= int64(count)
				}
			}
			if b.remaining == 0 {
				b.state = physicalH1Done
				if b.chunked {
					b.state = physicalH1ChunkCR
				}
			}
		case physicalH1ChunkLine, physicalH1Trailers:
			b.line = append(b.line, payload[0])
			payload = payload[1:]
			if len(b.line) > maxHTTPHeaderBytes+4096 {
				return errPhysicalH2Binding
			}
			if !bytes.HasSuffix(b.line, []byte("\r\n")) {
				continue
			}
			line := string(b.line[:len(b.line)-2])
			b.line = b.line[:0]
			if b.state == physicalH1Trailers {
				if line == "" {
					b.state = physicalH1Done
				}
				continue
			}
			size, _, _ := strings.Cut(line, ";")
			length, err := strconv.ParseUint(size, 16, 63)
			if err != nil || size == "" {
				return errPhysicalH2Binding
			}
			b.remaining, b.state = int64(length), physicalH1ChunkData
			if length == 0 {
				b.state = physicalH1Trailers
			}
		case physicalH1ChunkCR:
			if payload[0] != '\r' {
				return errPhysicalH2Binding
			}
			payload, b.state = payload[1:], physicalH1ChunkLF
		case physicalH1ChunkLF:
			if payload[0] != '\n' {
				return errPhysicalH2Binding
			}
			payload, b.state = payload[1:], physicalH1ChunkLine
		case physicalH1Done:
			return nil
		default:
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func (*physicalHTTP1Body) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
