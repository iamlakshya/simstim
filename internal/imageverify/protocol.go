// Package imageverify defines the bounded offline image verifier protocol.
// Its data grants no authority without the client's original checked process.
package imageverify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Version identifies the sole supported public protocol.
const Version = 1

// Role identifies the offline signature verifier's one executable role.
const Role = "image-verifier"

// MaxRequest bounds a complete encoded request before parsing or writing.
const MaxRequest = 18 << 20

// MaxResponse bounds the verifier's successful encoded response.
const MaxResponse = 1024

// MaxDiagnostic bounds the verifier's failure output.
const MaxDiagnostic = 8192

// MaxDocument bounds each decoded root, bundle or legacy payload.
const MaxDocument = 4 << 20

// RequestV1 carries all bytes needed to verify one exact artifact offline.
// Nonce binds the response to this original process invocation.
type RequestV1 struct {
	Version     int    `json:"version"`
	Role        string `json:"role"`
	Nonce       string `json:"nonce"`
	Digest      string `json:"digest"`
	Identity    string `json:"identity"`
	Issuer      string `json:"issuer"`
	TrustedRoot []byte `json:"trusted_root"`
	Bundle      []byte `json:"bundle"`
	Payload     []byte `json:"payload,omitempty"`
}

// ResponseV1 exists only after successful verification. Its request digest
// binds the exact input, including nonce, policy, root and signature bytes.
type ResponseV1 struct {
	Version       int    `json:"version"`
	Role          string `json:"role"`
	HelperVersion string `json:"helper_version"`
	RequestSHA256 string `json:"request_sha256"`
}

// Validate checks bounded and canonical request fields before verification.
func (r RequestV1) Validate() error {
	if r.Version != Version || r.Role != Role || !hexDigest(r.Nonce) || len(r.Digest) != 71 || !strings.HasPrefix(r.Digest, "sha256:") || !hexDigest(r.Digest[7:]) || r.Identity == "" || r.Issuer == "" || len(r.Identity) > 4096 || len(r.Issuer) > 4096 || strings.ContainsAny(r.Identity+r.Issuer, "\x00\r\n") || len(r.TrustedRoot) == 0 || len(r.TrustedRoot) > MaxDocument || len(r.Bundle) == 0 || len(r.Bundle) > MaxDocument || len(r.Payload) > MaxDocument {
		return fmt.Errorf("invalid offline image verification request")
	}
	return nil
}

func hexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// RequestDigest returns the SHA-256 of the complete original wire message.
func RequestDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// ReadRequest consumes one bounded strict request and its final EOF.
func ReadRequest(reader io.Reader) (RequestV1, []byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxRequest+1))
	if err != nil {
		return RequestV1{}, nil, err
	}
	if len(data) > MaxRequest {
		return RequestV1{}, nil, fmt.Errorf("offline image verification request exceeds its bound")
	}
	var request RequestV1
	if err := decodeOne(data, &request); err != nil {
		return RequestV1{}, nil, err
	}
	if err := request.Validate(); err != nil {
		return RequestV1{}, nil, err
	}
	return request, data, nil
}

// CheckResponse accepts only the exact original request's successful result.
func CheckResponse(data []byte, version string, request []byte) error {
	if len(data) == 0 || len(data) > MaxResponse {
		return fmt.Errorf("offline image verification response exceeds its bound")
	}
	var response ResponseV1
	if err := decodeOne(data, &response); err != nil {
		return err
	}
	if response.Version != Version || response.Role != Role || response.HelperVersion != version || version == "" || response.RequestSHA256 != RequestDigest(request) {
		return fmt.Errorf("offline image verification response differs from its original request or executable version")
	}
	return nil
}

func decodeOne(data []byte, out any) error {
	var fields []string
	switch out.(type) {
	case *RequestV1:
		fields = []string{"version", "role", "nonce", "digest", "identity", "issuer", "trusted_root", "bundle", "payload"}
	case *ResponseV1:
		fields = []string{"version", "role", "helper_version", "request_sha256"}
	default:
		return fmt.Errorf("unknown offline image verification message")
	}
	if err := uniqueJSONFields(data, fields); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("offline image verification message must contain one object")
	}
	return nil
}

func uniqueJSONFields(data []byte, fields []string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return fmt.Errorf("offline image verification message needs one object")
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field] = true
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !allowed[key] {
			return fmt.Errorf("offline image verification message has an unknown or repeated field")
		}
		seen[key] = true
		if err := decoder.Decode(new(json.RawMessage)); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
