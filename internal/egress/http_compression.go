package egress

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxDecodedHTTPBody = 64 << 20

type decodedHTTPBody struct {
	reader    io.ReadCloser
	original  io.ReadCloser
	remaining int64
	finished  bool
	finishErr error
}

func decodeHTTPBody(body io.ReadCloser, encoding string) (io.ReadCloser, bool, error) {
	encoding = strings.ToLower(strings.TrimSpace(encoding))
	if encoding == "" || encoding == "identity" {
		return body, false, nil
	}
	var reader io.ReadCloser
	var err error
	switch encoding {
	case "gzip":
		reader, err = gzip.NewReader(body)
	case "deflate":
		buffer := bufio.NewReader(body)
		header, peekErr := buffer.Peek(2)
		if peekErr != nil {
			return nil, false, fmt.Errorf("read compressed body: %w", peekErr)
		}
		if header[0]&15 == 8 && (int(header[0])<<8|int(header[1]))%31 == 0 {
			reader, err = zlib.NewReader(buffer)
		} else {
			reader = flate.NewReader(buffer)
		}
	default:
		return nil, false, errors.New("unsupported content encoding. Try: send an identity, gzip or deflate body")
	}
	if err != nil {
		return nil, false, fmt.Errorf("decode HTTP body: %w", err)
	}
	return &decodedHTTPBody{reader: reader, original: body, remaining: maxDecodedHTTPBody}, true, nil
}

func (b *decodedHTTPBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.finished {
		return 0, b.finishErr
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.reader.Read(probe[:])
		if n != 0 {
			b.finished, b.finishErr = true, errors.New("decoded HTTP body exceeds 64 MiB. Try: reduce the uncompressed body")
			return 0, b.finishErr
		}
		if errors.Is(err, io.EOF) {
			err = b.finishMessage()
		}
		return 0, err //nolint:wrapcheck // an io.Reader passes through its underlying Read error
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= int64(n)
	if errors.Is(err, io.EOF) {
		err = b.finishMessage()
	}
	return n, err //nolint:wrapcheck // an io.Reader passes through its underlying Read error
}

func (b *decodedHTTPBody) finishMessage() error {
	// A deflate decoder can finish before its HTTP body reaches EOF. One
	// bounded probe completes framing/trailers or refuses unconsumed data.
	// Never let Close silently drain and populate fields after validation.
	var probe [1]byte
	n, err := b.original.Read(probe[:])
	if n != 0 {
		err = errors.New("compressed HTTP body has trailing data. Try: send one complete encoded body")
	}
	if n == 0 && err == nil {
		return nil
	}
	b.finished, b.finishErr = true, err
	return err //nolint:wrapcheck // an io.Reader passes through its underlying EOF or failure
}

func (b *decodedHTTPBody) Close() error {
	return errors.Join(b.reader.Close(), b.original.Close())
}
