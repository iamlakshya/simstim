package egress

import (
	"errors"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

const maxHTTPQueryBytes = 8 << 10

var errInvalidHTTPPath = errors.New("invalid HTTP path. Try: use a canonical URL path")

func canonicalHTTPRequest(req *http.Request, hdr StreamHeader, scheme string) (Request, error) {
	if req.URL == nil || req.URL.User != nil || req.URL.Opaque != "" || req.URL.Fragment != "" || strings.Contains(req.RequestURI, "#") {
		return Request{}, errors.New("invalid HTTP target. Try: use a URL without credentials or fragments")
	}
	if !api.ValidHTTPMethod(req.Method) || req.Method == http.MethodConnect {
		return Request{}, errors.New("invalid intercepted method. Try: send an ordinary HTTP request inside TLS")
	}
	if len(req.URL.RawQuery) > maxHTTPQueryBytes {
		return Request{}, errors.New("HTTP query exceeds 8 KiB. Try: shorten the URL query")
	}
	if req.URL.Scheme != "" && !strings.EqualFold(req.URL.Scheme, scheme) {
		return Request{}, errors.New("HTTP URL scheme differs from the stream. Try: use the same scheme as the tunnel")
	}
	if scheme == "https" && req.ProtoMajor >= 2 && req.TLS == nil {
		return Request{}, errors.New("HTTP/2 scheme differs from TLS. Try: use the HTTPS scheme inside TLS")
	}
	if len(req.Header.Values("Host")) > 1 {
		return Request{}, errors.New("duplicate Host header. Try: send one authority")
	}
	if values := req.Header.Values("Host"); len(values) == 1 && values[0] == "" {
		return Request{}, errors.New("empty Host header. Try: send the tunnel's authority")
	}
	authorities := append([]string{req.Host, req.URL.Host}, req.Header.Values("Host")...)
	for _, authority := range authorities {
		if authority == "" {
			continue
		}
		host, port, err := parseHostHeader(authority, hdr.Port)
		if err != nil || host != hdr.Host || port != hdr.Port {
			return Request{}, errors.New("HTTP authority differs from TLS destination. Try: use the tunnel's host and port")
		}
	}
	if req.Host == "" {
		return Request{}, errors.New("HTTP request has no authority. Try: send a Host or :authority field")
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	path, err := canonicalPath(path)
	if err != nil {
		return Request{}, errInvalidHTTPPath
	}
	req.URL.Scheme = scheme
	req.URL.Host = net.JoinHostPort(hdr.Host, strconv.Itoa(hdr.Port))
	req.URL.Path, req.URL.RawPath, req.URL.Opaque = path, "", ""
	req.Host = req.URL.Host
	req.Header.Del("Host")
	req.RequestURI = ""
	return Request{Host: hdr.Host, Port: hdr.Port, Method: req.Method, Path: path, query: req.URL.RawQuery, scheme: scheme, forceQuery: req.URL.ForceQuery}, nil
}

func validateInspectedFraming(req *http.Request) error {
	if req.ProtoMajor < 2 {
		return nil // the raw ingress guard has already checked the H1 fields
	}
	if len(req.Header.Values("Content-Length")) > 1 || len(req.TransferEncoding) != 0 || len(req.Header.Values("Transfer-Encoding")) != 0 {
		return errors.New("invalid HTTP/2 request framing. Try: use one body length and no transfer coding")
	}
	declared := make(map[string]bool)
	for key := range req.Trailer {
		if !validRequestTrailerName(key) {
			return errors.New("forbidden HTTP/2 trailer. Try: remove routing, authentication and framing trailers")
		}
		declared[textproto.CanonicalMIMEHeaderKey(key)] = true
	}
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &proxyRequestBody{reader: req.Body, request: req, declared: declared, chunked: true}
	}
	return nil
}
