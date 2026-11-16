package egress

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

type requestSecretHeaderBag struct {
	values map[string][]string
	keys   map[string]int
}

func (requestSecretHeaderBag) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func requestSecretHeaders(input http.Header) (requestSecretHeaderBag, *requestSecretFailure) {
	output := requestSecretHeaderBag{values: make(map[string][]string, len(input)), keys: make(map[string]int, len(input))}
	size := 0
	for key, values := range input {
		if !api.ValidHTTPToken(key) {
			return requestSecretHeaderBag{}, requestSecretRefusal(http.StatusBadRequest, "bad_request")
		}
		size += len(key) + 4
		canonical := http.CanonicalHeaderKey(key)
		output.keys[canonical]++
		if _, ok := output.values[canonical]; !ok {
			output.values[canonical] = nil
		}
		for _, value := range values {
			size += len(value) + 2
			if size > maxHTTPHeaderBytes {
				return requestSecretHeaderBag{}, requestSecretRefusal(http.StatusBadRequest, "bad_request")
			}
			for i := range len(value) {
				if value[i] < 0x20 && value[i] != '\t' || value[i] == 0x7f {
					return requestSecretHeaderBag{}, requestSecretRefusal(http.StatusBadRequest, "bad_request")
				}
			}
			output.values[canonical] = append(output.values[canonical], value)
		}
		if size > maxHTTPHeaderBytes {
			return requestSecretHeaderBag{}, requestSecretRefusal(http.StatusBadRequest, "bad_request")
		}
	}
	return output, nil
}

// The standard Basic parser determines the first username/password separator.
// Strict decoding and canonical re-encoding exclude alternate padding/whitespace.
func requestSecretBasic(value string) (user, password string, ok bool) {
	if len(value) < 6 || !strings.EqualFold(value[:6], "Basic ") || len(value) > maxHTTPHeaderBytes {
		return "", "", false
	}
	encoded := value[6:]
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return "", "", false
	}
	request := &http.Request{Header: http.Header{"Authorization": []string{value}}}
	return request.BasicAuth()
}

func (s *requestSecretSnapshot) tripwire(text string, fold bool) *requestSecretFailure {
	return s.tripwireProjection(text, fold, false, false)
}

func (s *requestSecretSnapshot) tripwireQuery(text string) *requestSecretFailure {
	return s.tripwireProjection(text, false, false, true)
}

func (s *requestSecretSnapshot) tripwireProjection(text string, fold, basic, query bool) *requestSecretFailure {
	return s.tripwireProjectionStage(text, fold, basic, query, requestTripwireStrict)
}

func (s *requestSecretSnapshot) tripwireProjectionStage(text string, fold, basic, query bool, stage requestTripwireStage) *requestSecretFailure {
	if s == nil || len(s.tripwires) == 0 {
		return nil
	}
	// URL.String may escape a valid raw UTF-8 target to at most three times
	// its original bytes. Raw targets and header fields are capped separately.
	if len(text) > 3*maxHTTPHeaderBytes {
		return requestSecretRefusal(http.StatusBadRequest, "bad_request")
	}
	for pass := 0; pass <= maxScrubDecodePasses; pass++ {
		projection := text
		if fold {
			projection = strings.ToLower(projection)
		}
		for index, value := range s.tripwires {
			needle := string(value)
			if fold {
				needle = strings.ToLower(needle)
			}
			if strings.Contains(projection, needle) {
				return s.tripwireRefusal(index)
			}
		}
		if basic && len(text) >= 6 && strings.EqualFold(text[:6], "Basic ") {
			user, password, ok := requestSecretBasic(text)
			if !ok && stage == requestTripwireStrict {
				return requestSecretRefusal(http.StatusBadRequest, "bad_request")
			}
			if ok {
				if failure := s.tripwire(user+":"+password, false); failure != nil {
					return failure
				}
			}
		}
		if !strings.Contains(text, "%") && (!query || !strings.Contains(text, "+")) {
			return nil
		}
		next, err := url.PathUnescape(text)
		if query {
			next, err = url.QueryUnescape(text)
		}
		if err != nil {
			var changed bool
			input := text
			if query {
				input = strings.ReplaceAll(input, "+", " ")
			}
			next, changed = decodeAuditEscapes(input)
			changed = changed || input != text
			if !changed {
				return nil
			}
		}
		if next == text {
			return nil
		}
		if pass == maxScrubDecodePasses {
			return requestSecretRefusal(http.StatusBadRequest, "bad_request")
		}
		text = next
	}
	return nil
}

func (s *requestSecretSnapshot) tripwireHeaderValue(value string) *requestSecretFailure {
	// Basic is a recognised encoding only. It never authorises an otherwise
	// unknown header or protocol, and malformed forms gain no exemption.
	return s.tripwireHeaderValueStage(value, requestTripwireStrict)
}

func (s *requestSecretSnapshot) tripwireHeaderValueStage(value string, stage requestTripwireStage) *requestSecretFailure {
	return s.tripwireProjectionStage(value, false, true, false, stage)
}

// Trailers are a late pure check only. Main must call this before emitting
// final request trailers. It never supplies real values to a trailer field.
func (s *requestSecretSnapshot) checkRequestSecretTrailers(trailers http.Header, authority string) *requestSecretFailure {
	bag, failure := requestSecretHeaders(trailers)
	if failure != nil {
		return failure
	}
	for key, values := range bag.values {
		if failure := s.tripwire(key, true); failure != nil {
			return failure
		}
		if s != nil && len(s.authorities[authority]) > 0 && (s.credentialHeaders[key] || key == "Proxy-Authorization") {
			return requestSecretRefusal(http.StatusForbidden, "secret_misuse")
		}
		for _, value := range values {
			if failure := s.tripwireHeaderValue(value); failure != nil {
				return failure
			}
		}
	}
	return nil
}
