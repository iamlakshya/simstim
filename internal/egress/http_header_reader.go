package egress

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
)

// Inspect raw fields without consuming them, then let the standard parser use
// the same bounded reader for headers, bodies and later pipelined requests.
// Rebuilding a reader chain for every request would retain its predecessors.
func readBufferedHTTPRequest(reader *bufio.Reader) (*http.Request, textproto.MIMEHeader, error) {
	observed, end := 0, 0
	for end == 0 {
		if observed >= maxHTTPHeaderBytes {
			return nil, nil, errors.New("HTTP header too large")
		}
		if reader.Buffered() == observed {
			if _, err := reader.Peek(observed + 1); err != nil {
				return nil, nil, fmt.Errorf("read HTTP request header: %w", err)
			}
		}
		data, err := reader.Peek(reader.Buffered())
		if err != nil {
			return nil, nil, fmt.Errorf("inspect HTTP request header: %w", err)
		}
		for index := observed; index < len(data) && index < maxHTTPHeaderBytes; index++ {
			if data[index] == '\n' && (index >= 1 && data[index-1] == '\n' || index >= 3 && data[index-3] == '\r' && data[index-2] == '\n' && data[index-1] == '\r') {
				end = index + 1
				break
			}
		}
		observed = len(data)
	}
	data, err := reader.Peek(end)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect complete HTTP header: %w", err)
	}
	raw, err := parseRawHeaders(data)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.ReadRequest(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("parse HTTP request: %w", err)
	}
	return request, raw, nil
}
