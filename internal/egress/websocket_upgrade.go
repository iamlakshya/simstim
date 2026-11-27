package egress

import (
	"crypto/sha1" // RFC 6455 requires SHA-1 for this public handshake checksum.
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// prepareBoundWebSocketRequest runs on the canonical outbound request before
// origin headers. Bound messages use no negotiated extension. Ordinary HTTP and
// the existing unbound byte-preserving upgrade path do not call these helpers.
func prepareBoundWebSocketRequest(request *http.Request) (bool, error) {
	if !webSocketHeaderToken(request.Header, "Connection", "upgrade") {
		return false, nil
	}
	upgrade, unique := webSocketSingleHeader(request.Header, "Upgrade")
	if !unique {
		return false, errors.New("ambiguous WebSocket upgrade protocol. Try: send one upgrade protocol")
	}
	if !strings.EqualFold(upgrade, "websocket") {
		return false, nil
	}
	if request.Method != http.MethodGet || request.ProtoMajor != 1 || request.ProtoMinor < 1 {
		return false, errors.New("invalid WebSocket upgrade method or transport. Try: use a classic HTTP/1.1 GET upgrade")
	}
	if _, err := webSocketRequestKey(request); err != nil {
		return false, err
	}
	if version, ok := webSocketSingleHeader(request.Header, "Sec-WebSocket-Version"); !ok || version != "13" {
		return false, errors.New("unsupported WebSocket version. Try: use WebSocket version 13")
	}
	if _, err := webSocketProtocols(request.Header); err != nil {
		return false, err
	}
	for name := range request.Header {
		if strings.EqualFold(name, "Sec-WebSocket-Extensions") {
			delete(request.Header, name)
		}
	}
	return true, nil
}

// validateBoundWebSocketResponse must finish before the guest receives 101.
// The caller leaves the single request lease in place through upgraded IO.
func validateBoundWebSocketResponse(request *http.Request, response *http.Response) error {
	if response.StatusCode != http.StatusSwitchingProtocols || !webSocketHeaderToken(response.Header, "Connection", "upgrade") {
		return errors.New("invalid WebSocket upgrade response. Try: check the origin's WebSocket support")
	}
	upgrade, ok := webSocketSingleHeader(response.Header, "Upgrade")
	if !ok || !strings.EqualFold(upgrade, "websocket") {
		return errors.New("origin selected a different upgrade protocol. Try: request a WebSocket upgrade")
	}
	for name, values := range response.Header {
		if strings.EqualFold(name, "Content-Encoding") {
			for _, value := range values {
				if value != "" && !strings.EqualFold(strings.TrimSpace(value), "identity") {
					return errors.New("encoded WebSocket upgrade response. Try: use an uncompressed WebSocket connection")
				}
			}
		}
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Transfer-Encoding") || strings.EqualFold(name, "Trailer") {
			return errors.New("WebSocket upgrade carries HTTP body framing. Try: send the upgrade without an HTTP response body")
		}
	}
	if response.ContentLength > 0 || len(response.TransferEncoding) != 0 || len(response.Trailer) != 0 {
		return errors.New("WebSocket upgrade carries HTTP body framing. Try: send the upgrade without an HTTP response body")
	}
	key, err := webSocketRequestKey(request)
	if err != nil {
		return err
	}
	// This hash is the RFC 6455 section 4.2.2 public handshake checksum, not a
	// credential, signature, policy decision or cryptographic trust primitive.
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept, ok := webSocketSingleHeader(response.Header, "Sec-WebSocket-Accept")
	if !ok || accept != base64.StdEncoding.EncodeToString(sum[:]) {
		return errors.New("origin returned an invalid WebSocket handshake. Try: check the origin's WebSocket endpoint")
	}
	for name, values := range response.Header {
		if strings.EqualFold(name, "Sec-WebSocket-Extensions") {
			for _, value := range values {
				if strings.TrimSpace(value) != "" {
					return errors.New("origin negotiated an unrequested WebSocket extension. Try: use an uncompressed WebSocket connection")
				}
			}
			delete(response.Header, name)
		}
	}
	selected, err := webSocketProtocols(response.Header)
	if err != nil || len(selected) > 1 {
		return errors.New("origin selected an invalid WebSocket subprotocol. Try: select one requested subprotocol")
	}
	if len(selected) == 1 {
		offered, err := webSocketProtocols(request.Header)
		if err != nil {
			return err
		}
		found := false
		for _, protocol := range offered {
			found = found || protocol == selected[0]
		}
		if !found {
			return errors.New("origin selected an unrequested WebSocket subprotocol. Try: select one requested subprotocol")
		}
	}
	return nil
}

func webSocketRequestKey(request *http.Request) (string, error) {
	key, ok := webSocketSingleHeader(request.Header, "Sec-WebSocket-Key")
	decoded, err := base64.StdEncoding.Strict().DecodeString(key)
	if !ok || err != nil || len(decoded) != 16 || base64.StdEncoding.EncodeToString(decoded) != key {
		return "", errors.New("invalid WebSocket handshake key. Try: send one base64-encoded 16-byte key")
	}
	return key, nil
}

func webSocketSingleHeader(header http.Header, name string) (string, bool) {
	var result string
	count := 0
	for key, values := range header {
		if strings.EqualFold(key, name) {
			for _, value := range values {
				result = strings.Trim(value, " \t")
				count++
			}
		}
	}
	return result, count == 1
}

func webSocketHeaderToken(header http.Header, name, token string) bool {
	for key, values := range header {
		if strings.EqualFold(key, name) {
			for _, value := range values {
				for _, part := range strings.Split(value, ",") {
					if strings.EqualFold(strings.Trim(part, " \t"), token) {
						return true
					}
				}
			}
		}
	}
	return false
}

func webSocketProtocols(header http.Header) ([]string, error) {
	var protocols []string
	for key, values := range header {
		if !strings.EqualFold(key, "Sec-WebSocket-Protocol") {
			continue
		}
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				part = strings.Trim(part, " \t")
				if part == "" {
					return nil, errors.New("empty WebSocket subprotocol. Try: send valid HTTP token subprotocol names")
				}
				if !api.ValidHTTPMethod(part) {
					return nil, errors.New("invalid WebSocket subprotocol. Try: send valid HTTP token subprotocol names")
				}
				for _, prior := range protocols {
					if part == prior {
						return nil, errors.New("duplicate WebSocket subprotocol. Try: list each requested subprotocol once")
					}
				}
				protocols = append(protocols, part)
			}
		}
	}
	return protocols, nil
}
