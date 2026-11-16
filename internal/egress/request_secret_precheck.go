package egress

import (
	"net"
	"net/http"
	"strconv"
)

// This is a pure pre-policy tripwire, not permission to send a request. Main
// runs framing/canonical validation, this check, the SAME policy precheck,
// then prepareRequestSecrets pin/401 and actual per-request reservation.
// Only public stand-ins/binding metadata are examined here. No real value,
// Armed state, once ledger, origin I/O or used-identity plan is touched.
func checkRequestSecretTripwires(snapshot *requestSecretSnapshot, request *http.Request, original requestSecretRawView, checked Request, scheme string) *requestSecretFailure {
	raw, canonical, failure := requestSecretInputs(request, original, checked, scheme)
	if failure != nil {
		return failure
	}
	protected := snapshot.allowedRequestSecretHeaders(raw, canonical, checked, scheme)
	return snapshot.scanRequestSecretTripwires(request, original, raw, canonical, protected, requestTripwirePrePolicy)
}

func requestSecretInputs(request *http.Request, original requestSecretRawView, checked Request, scheme string) (requestSecretHeaderBag, requestSecretHeaderBag, *requestSecretFailure) {
	if request == nil || request.URL == nil {
		return requestSecretHeaderBag{}, requestSecretHeaderBag{}, requestSecretRefusal(http.StatusBadRequest, "bad_request")
	}
	canonical, failure := requestSecretHeaders(request.Header)
	if failure != nil {
		return requestSecretHeaderBag{}, requestSecretHeaderBag{}, failure
	}
	raw, failure := requestSecretHeaders(original.Headers)
	if failure != nil {
		return requestSecretHeaderBag{}, requestSecretHeaderBag{}, failure
	}
	if failure := validateRequestSecretAuthority(request, original, raw, checked, scheme); failure != nil {
		return requestSecretHeaderBag{}, requestSecretHeaderBag{}, failure
	}
	return raw, canonical, nil
}

// Classification gives one complete known named sentinel form its narrow
// tripwire exemption. Unknown/duplicate credentials receive NO exemption but
// are not independently pinned or armed here. A nil return never grants access.
func (s *requestSecretSnapshot) allowedRequestSecretHeaders(raw, canonical requestSecretHeaderBag, checked Request, scheme string) map[string]int {
	protected := make(map[string]int)
	if s == nil || scheme != "https" || checked.Port != 443 {
		return protected
	}
	authority := net.JoinHostPort(checked.Host, strconv.Itoa(checked.Port))
	for key, values := range canonical.values {
		rawValues, ok := raw.values[key]
		if !ok || canonical.keys[key] != 1 || raw.keys[key] != 1 || len(values) != 1 || len(rawValues) != 1 || values[0] != rawValues[0] {
			continue
		}
		found := -1
		for _, index := range s.authorities[authority] {
			entry := s.entries[index]
			if entry.source.Binding.Header == key && requestSecretIdentity(entry, values[0]) {
				if found >= 0 {
					found = -1
					break
				}
				found = index
			}
		}
		if found >= 0 {
			protected[key] = found
		}
	}
	return protected
}

type requestTripwireStage uint8

const (
	requestTripwirePrePolicy requestTripwireStage = iota
	requestTripwireStrict
)

func (s *requestSecretSnapshot) scanRequestSecretTripwires(request *http.Request, original requestSecretRawView, raw, canonical requestSecretHeaderBag, protected map[string]int, stage requestTripwireStage) *requestSecretFailure {
	if s == nil || len(s.tripwires) == 0 {
		return nil
	}
	for _, value := range []string{original.Target, request.URL.String()} {
		if failure := s.tripwire(value, false); failure != nil {
			return failure
		}
	}
	for _, value := range []string{original.Authority, request.Host, request.URL.Host} {
		if failure := s.tripwire(value, true); failure != nil {
			return failure
		}
	}
	for _, query := range []string{request.URL.RawQuery, rawRequestSecretQuery(original.Target)} {
		if failure := s.tripwireQuery(query); failure != nil {
			return failure
		}
	}
	for _, headers := range []requestSecretHeaderBag{raw, canonical} {
		for key, values := range headers.values {
			if failure := s.tripwire(key, true); failure != nil {
				return failure
			}
			// A named bound-header exemption can never exempt a decoy.
			if s.decoyView != nil {
				for _, value := range values {
					if failure := s.decoyView.tripwireHeaderValueStage(value, stage); failure != nil {
						return failure
					}
				}
			}
			if index, ok := protected[key]; ok {
				entry := s.entries[index]
				if entry.kind == "bearer" {
					if failure := s.tripwire(values[0][:7], false); failure != nil {
						return failure
					}
				}
				if entry.kind == "basic" {
					if failure := s.tripwire(values[0][:6], false); failure != nil {
						return failure
					}
					if failure := s.tripwire(entry.user+":", false); failure != nil {
						return failure
					}
				}
				continue
			}
			for _, value := range values {
				if failure := s.tripwireHeaderValueStage(value, stage); failure != nil {
					return failure
				}
			}
		}
	}
	trailers, failure := requestSecretHeaders(request.Trailer)
	if failure != nil {
		return failure
	}
	for key, values := range trailers.values {
		if failure := s.tripwire(key, true); failure != nil {
			return failure
		}
		for _, value := range values {
			if failure := s.tripwireHeaderValueStage(value, stage); failure != nil {
				return failure
			}
		}
	}
	return nil
}
