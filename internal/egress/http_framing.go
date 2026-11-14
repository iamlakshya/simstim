package egress

import (
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"strings"
)

func validateRequestFraming(req *http.Request, raw textproto.MIMEHeader) error {
	lengths, codings := raw["Content-Length"], raw["Transfer-Encoding"]
	if len(lengths) > 1 {
		return errors.New("duplicate Content-Length header. Try: send one request body length")
	}
	if len(lengths) > 0 && len(codings) > 0 {
		return errors.New("both Content-Length and Transfer-Encoding set. Try: use one request body framing")
	}
	if req.Method == http.MethodConnect {
		if len(codings) != 0 || len(req.TransferEncoding) != 0 || (len(lengths) == 1 && strings.TrimSpace(lengths[0]) != "0") {
			return errors.New("CONNECT requests cannot carry a framed body. Try: send CONNECT without a request body")
		}
	}
	chunked := len(req.TransferEncoding) == 1 && req.TransferEncoding[0] == "chunked"
	if len(codings) != 0 || len(req.TransferEncoding) != 0 {
		if !req.ProtoAtLeast(1, 1) || len(lengths) != 0 || len(codings) != 1 || !strings.EqualFold(strings.TrimSpace(codings[0]), "chunked") || !chunked {
			return errors.New("unsupported Transfer-Encoding header. Try: send one chunked transfer coding")
		}
	}
	declared, err := declaredRequestTrailers(raw["Trailer"], req.Trailer, chunked)
	if err != nil {
		return err
	}
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &proxyRequestBody{reader: req.Body, request: req, declared: declared, chunked: chunked}
	}
	return nil
}

func declaredRequestTrailers(values []string, parsed http.Header, chunked bool) (map[string]bool, error) {
	if (len(values) != 0 || len(parsed) != 0) && !chunked {
		return nil, errors.New("request trailers require chunked framing. Try: declare trailers on a chunked request")
	}
	declared := make(map[string]bool)
	for _, value := range values {
		for _, field := range strings.Split(value, ",") {
			field = strings.TrimSpace(field)
			if !validRequestTrailerName(field) {
				return nil, errors.New("invalid request trailer declaration. Try: use trailer fields without routing, authentication or framing information")
			}
			declared[textproto.CanonicalMIMEHeaderKey(field)] = true
		}
	}
	if err := validateFinalRequestTrailers(parsed, declared); err != nil {
		return nil, err
	}
	return declared, nil
}

func validRequestTrailerName(name string) bool { return validHTTPPolicyTrailerName(name) }

func validateFinalRequestTrailers(trailers http.Header, declared map[string]bool) error {
	for name := range trailers {
		if !validRequestTrailerName(name) || !declared[textproto.CanonicalMIMEHeaderKey(name)] {
			return errors.New("undeclared or forbidden request trailer. Try: send only the declared trailer fields")
		}
	}
	return nil
}

// The request borrows its stream. ServeFlow closes that stream, so closing
// this reader must never drain an unfinished body from the guest.
type proxyRequestBody struct {
	reader   io.Reader
	request  *http.Request
	declared map[string]bool
	chunked  bool
}

func (b *proxyRequestBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) && b.chunked {
		// net/http has populated request.Trailer by this point. Return an
		// error before its transfer writer emits the final chunk or fields.
		if trailerErr := validateFinalRequestTrailers(b.request.Trailer, b.declared); trailerErr != nil {
			return n, trailerErr
		}
	}
	return n, err //nolint:wrapcheck // an io.Reader passes through its underlying Read error
}

func (*proxyRequestBody) Close() error { return nil }
