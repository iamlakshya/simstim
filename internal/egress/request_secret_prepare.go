package egress

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Main calls this after the same nonconsuming canonical network eligibility
// check and before its actual per-request reservation/origin write. The plan
// retains this immutable generation. No policyAllowed flag or authority token
// is accepted and no origin operation, body read or request mutation occurs.
func prepareRequestSecrets(snapshot *requestSecretSnapshot, request *http.Request, original requestSecretRawView, checked Request, scheme string) (requestSecretPlan, *requestSecretFailure) {
	plan := requestSecretPlan{generation: snapshot}
	raw, canonical, failure := requestSecretInputs(request, original, checked, scheme)
	if failure != nil {
		return requestSecretPlan{}, failure
	}
	protected := snapshot.allowedRequestSecretHeaders(raw, canonical, checked, scheme)
	if failure := snapshot.scanRequestSecretTripwires(request, original, raw, canonical, protected, requestTripwireStrict); failure != nil {
		return requestSecretPlan{}, failure
	}
	if _, ok := canonical.values["Proxy-Authorization"]; ok {
		return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
	}
	if _, ok := raw.values["Proxy-Authorization"]; ok {
		return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
	}
	if snapshot == nil {
		return plan, nil
	}
	authority := net.JoinHostPort(checked.Host, strconv.Itoa(checked.Port))
	active := snapshot.authorities[authority]
	secure := scheme == "https" && checked.Port == 443
	if failure := snapshot.checkRequestSecretTrailers(request.Trailer, authority); failure != nil {
		return requestSecretPlan{}, failure
	}
	matches := make(map[string]int)
	if len(active) > 0 {
		for key := range snapshot.credentialHeaders {
			values, hasCanonical := canonical.values[key]
			rawValues, hasRaw := raw.values[key]
			if hasCanonical != hasRaw {
				return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
			}
			if !hasCanonical {
				continue
			}
			if !secure || canonical.keys[key] != 1 || raw.keys[key] != 1 || len(values) != 1 || len(rawValues) != 1 || values[0] != rawValues[0] {
				return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
			}
			found, ok := protected[key]
			if !ok {
				return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
			}
			matches[key] = found
		}
	}
	if secure && len(active) > 0 {
		if len(matches) > 0 {
			for _, index := range matches {
				if !snapshot.entries[index].source.Armed {
					return requestSecretPlan{}, requestSecretRefusal(http.StatusUnauthorized, "secret_unarmed")
				}
			}
		} else {
			anyArmed := false
			for _, index := range active {
				anyArmed = anyArmed || snapshot.entries[index].source.Armed
			}
			if !anyArmed {
				return requestSecretPlan{}, requestSecretRefusal(http.StatusUnauthorized, "secret_unarmed")
			}
		}
	}
	if len(matches) > 0 && !secure {
		return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
	}
	for _, index := range active {
		entry := snapshot.entries[index]
		if matched, ok := matches[entry.source.Binding.Header]; !ok || matched != index {
			continue
		}
		value, ok := requestCredentialValue(entry.kind, entry.user, entry.source.Value)
		if !ok {
			return requestSecretPlan{}, requestSecretRefusal(http.StatusForbidden, "secret_misuse")
		}
		var removeKeys []string
		for key := range request.Header {
			if strings.EqualFold(key, entry.source.Binding.Header) {
				removeKeys = append(removeKeys, key)
			}
		}
		plan.replacements = append(plan.replacements, requestSecretReplacement{name: entry.source.Name, identity: entry.source.Identity, header: entry.source.Binding.Header, value: value, removeKeys: removeKeys})
		plan.names = append(plan.names, entry.aliases...)
		plan.identities = append(plan.identities, entry.source.Identity)
	}
	sort.Strings(plan.names)
	plan.names = slices.Compact(plan.names)
	sort.Strings(plan.identities)
	plan.identities = slices.Compact(plan.identities)
	return plan, nil
}

func sameRequestSecretIdentity(a, b requestSecretRecord) bool {
	return a.source.Identity == b.source.Identity && a.source.Armed == b.source.Armed && a.source.Value == b.source.Value && a.source.Sentinel == b.source.Sentinel && a.source.Binding.Header == b.source.Binding.Header && a.source.Binding.Format == b.source.Binding.Format && slices.Equal(a.source.Binding.Hosts, b.source.Binding.Hosts)
}

func requestSecretIdentity(entry requestSecretRecord, value string) bool {
	switch entry.kind {
	case "raw":
		return value == string(entry.source.Sentinel)
	case "bearer":
		return len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") && value[7:] == string(entry.source.Sentinel)
	case "basic":
		user, password, ok := requestSecretBasic(value)
		return ok && user == entry.user && password == string(entry.source.Sentinel)
	default:
		return false
	}
}

func rawRequestSecretQuery(target string) string {
	if u, err := url.ParseRequestURI(target); err == nil {
		return u.RawQuery
	}
	return ""
}

func validateRequestSecretAuthority(request *http.Request, original requestSecretRawView, rawHeaders requestSecretHeaderBag, checked Request, scheme string) *requestSecretFailure {
	if scheme != "http" && scheme != "https" || len(original.Target) > maxHTTPHeaderBytes || len(original.Authority) > maxHTTPHeaderBytes {
		return requestSecretRefusal(http.StatusBadRequest, "bad_request")
	}
	copyRequest := request.Clone(request.Context())
	actual, err := canonicalHTTPRequest(copyRequest, StreamHeader{Host: checked.Host, Port: checked.Port}, scheme)
	if err != nil || actual.Host != checked.Host || actual.Port != checked.Port || actual.Method != checked.Method || actual.Path != checked.Path {
		return requestSecretRefusal(http.StatusBadRequest, "bad_request")
	}
	if hosts, ok := rawHeaders.values["Host"]; ok && (rawHeaders.keys["Host"] != 1 || len(hosts) != 1 || hosts[0] == "") {
		return requestSecretRefusal(http.StatusBadRequest, "bad_request")
	}
	for _, authority := range append([]string{original.Authority}, rawHeaders.values["Host"]...) {
		if authority == "" {
			continue
		}
		host, port, err := parseHostHeader(authority, checked.Port)
		if err != nil || host != checked.Host || port != checked.Port {
			return requestSecretRefusal(http.StatusBadRequest, "bad_request")
		}
	}
	if original.Target != "" {
		u, err := url.ParseRequestURI(original.Target)
		if err != nil || u.User != nil || u.Opaque != "" || strings.Contains(original.Target, "#") {
			return requestSecretRefusal(http.StatusBadRequest, "bad_request")
		}
		if u.Scheme != "" && !strings.EqualFold(u.Scheme, scheme) {
			return requestSecretRefusal(http.StatusBadRequest, "bad_request")
		}
		if u.Host != "" {
			host, port, err := parseHostHeader(u.Host, checked.Port)
			if err != nil || host != checked.Host || port != checked.Port {
				return requestSecretRefusal(http.StatusBadRequest, "bad_request")
			}
		}
	}
	return nil
}
