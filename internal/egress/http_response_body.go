package egress

import (
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"strings"
)

// prepareHTTPResponse validates declarations before guest headers. Its outer
// reader validates actual trailers only after decoding and HTTP body EOF, so
// ReverseProxy cannot emit late forbidden metadata after policy inspection.
func prepareHTTPResponse(response *http.Response, method string) error {
	declared := make(map[string]bool)
	for key := range response.Trailer {
		if !validHTTPPolicyTrailerName(key) {
			return errors.New("forbidden response trailer. Try: keep routing, authentication and framing fields in response headers")
		}
		declared[textproto.CanonicalMIMEHeaderKey(key)] = true
	}
	for _, value := range response.Header.Values("Trailer") {
		for _, key := range strings.Split(value, ",") {
			key = strings.TrimSpace(key)
			if !validHTTPPolicyTrailerName(key) {
				return errors.New("forbidden response trailer declaration. Try: declare only safe response metadata")
			}
			declared[textproto.CanonicalMIMEHeaderKey(key)] = true
		}
		if response.ProtoMajor < 2 && len(response.TransferEncoding) == 0 {
			return errors.New("response trailers have no chunked framing. Try: send one valid response framing")
		}
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		if len(declared) != 0 {
			return errors.New("upgraded response cannot declare HTTP trailers. Try: keep upgrade metadata in headers")
		}
		return nil
	}
	if method == http.MethodHead || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotModified || response.Body == http.NoBody {
		if _, err := httpContentEncoding(response.Header); err != nil {
			return err
		}
		clearHTTPContentEncoding(response.Header)
		return nil
	}
	if len(declared) != 0 {
		// H2 permits a fixed length together with trailers. Downstream H1
		// needs chunked framing to retain those fields at the end.
		response.Header.Del("Content-Length")
		response.ContentLength = -1
	}
	body, decoded, err := decodeHTTPBody(response.Body, response.Header)
	if err != nil {
		return err
	}
	if decoded {
		response.Header.Del("Content-Encoding")
		response.Header.Del("Content-Length")
		response.ContentLength = -1
	}
	response.Body = &policyResponseBody{reader: body, response: response, declared: declared}
	return nil
}

type policyResponseBody struct {
	reader    io.ReadCloser
	response  *http.Response
	declared  map[string]bool
	finished  bool
	finishErr error
}

func (b *policyResponseBody) Read(p []byte) (int, error) {
	if b.finished {
		return 0, b.finishErr
	}
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) {
		for key := range b.response.Trailer {
			if !validHTTPPolicyTrailerName(key) || !b.declared[textproto.CanonicalMIMEHeaderKey(key)] {
				err = errors.New("undeclared or forbidden response trailer. Try: send only safe declared response metadata")
				break
			}
		}
		b.finished, b.finishErr = true, err
	}
	return n, err //nolint:wrapcheck // an io.Reader preserves EOF and its underlying failure
}

func (b *policyResponseBody) Close() error {
	return b.reader.Close() //nolint:wrapcheck // an io.Closer preserves its underlying result
}
