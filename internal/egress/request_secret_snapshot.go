package egress

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// These records are immutable projections of the sole member registry.
// They do not fetch values, store credentials or authorise a destination.
type requestBoundSecret struct {
	Name            string
	Identity        string
	Binding         api.SecretBind
	Value, Sentinel api.Secret
	Armed           bool
}
type requestSecretRecord struct {
	source     requestBoundSecret
	kind, user string
	aliases    []string
}
type requestSecretSnapshot struct {
	entries           []requestSecretRecord
	authorities       map[string][]int
	credentialHeaders map[string]bool
	tripwires         []api.Secret
}
type requestSecretRawView struct {
	Target, Authority string
	Headers           http.Header
}
type requestSecretReplacement struct {
	name, identity, header string
	value                  api.Secret
	removeKeys             []string
}
type requestSecretPlan struct {
	generation        *requestSecretSnapshot
	replacements      []requestSecretReplacement
	names, identities []string
}
type requestSecretFailure struct {
	status int
	detail *proto.Error
}

func (requestBoundSecret) Format(f fmt.State, _ rune)       { _, _ = io.WriteString(f, api.Redacted) }
func (requestSecretRecord) Format(f fmt.State, _ rune)      { _, _ = io.WriteString(f, api.Redacted) }
func (requestSecretSnapshot) Format(f fmt.State, _ rune)    { _, _ = io.WriteString(f, api.Redacted) }
func (requestSecretRawView) Format(f fmt.State, _ rune)     { _, _ = io.WriteString(f, api.Redacted) }
func (requestSecretReplacement) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (requestSecretPlan) Format(f fmt.State, _ rune)        { _, _ = io.WriteString(f, api.Redacted) }

func (p requestSecretPlan) usedNames() []string      { return append([]string(nil), p.names...) }
func (p requestSecretPlan) usedIdentities() []string { return append([]string(nil), p.identities...) }

func requestSecretRefusal(status int, code string) *requestSecretFailure {
	message, hint := "request credential is not permitted", "Use the session's bound credential in its named HTTPS header."
	if status == http.StatusBadRequest {
		message, hint = "request credential fields are malformed", "Send one canonical request authority and one unambiguous value per credential header."
	}
	if status == http.StatusUnauthorized {
		message, hint = "session credentials are not armed", "Attach from the trusted client to arm configured credentials. Re-mint server-issued credentials or start a fresh session."
	}
	return &requestSecretFailure{status: status, detail: &proto.Error{Code: code, Message: message, Hint: hint}}
}

func newRequestSecretSnapshot(entries []requestBoundSecret) (*requestSecretSnapshot, error) {
	snapshot := &requestSecretSnapshot{authorities: make(map[string][]int), credentialHeaders: map[string]bool{"Authorization": true, "X-Api-Key": true, "Api-Key": true}}
	owned := append([]requestBoundSecret(nil), entries...)
	sort.Slice(owned, func(i, j int) bool {
		if owned[i].Identity != owned[j].Identity {
			return owned[i].Identity < owned[j].Identity
		}
		return owned[i].Name < owned[j].Name
	})
	byIdentity := make(map[string]int, len(owned))
	for _, entry := range owned {
		if !validRegistryName(entry.Name) || entry.Identity == "" || entry.Sentinel == "" {
			return nil, memberError("bad_secret", "invalid derived request-secret identity", "Rebuild the session's approved secret generation.")
		}
		if err := checkRegistryValue(entry.Name, entry.Sentinel); err != nil {
			return nil, memberError("bad_secret", "invalid derived request-secret stand-in", "Rebuild the session's approved secret generation.")
		}
		if entry.Armed {
			if err := checkRegistryValue(entry.Name, entry.Value); err != nil {
				return nil, memberError("bad_secret", "invalid derived request-secret value", "Rebuild the session's approved secret generation.")
			}
		}
		binding, err := secretbind.Canonical(entry.Binding)
		if err != nil {
			return nil, memberError("bad_secret", "invalid derived request-secret binding", "Review the approved exact HTTPS credential binding.")
		}
		entry.Binding = binding
		kind, user, err := secretbind.ParseFormat(binding.Format)
		if err != nil {
			return nil, memberError("bad_secret", "invalid derived request-secret format", "Review the approved credential header format.")
		}
		if formatted, ok := requestCredentialValue(kind, user, entry.Sentinel); !ok || len(formatted)+len(binding.Header)+4 > maxHTTPHeaderBytes {
			return nil, memberError("bad_secret", "the credential stand-in cannot use its HTTP format", "Resume with a representable credential format.")
		}
		if entry.Armed {
			if len(entry.Value) != len(entry.Sentinel) || entry.Value == entry.Sentinel {
				return nil, memberError("bad_secret", "derived request-secret shape is inconsistent", "Rebuild the session's approved secret generation.")
			}
			if formatted, ok := requestCredentialValue(kind, user, entry.Value); !ok || len(formatted)+len(binding.Header)+4 > maxHTTPHeaderBytes {
				return nil, memberError("bad_secret", "the credential cannot use its HTTP format", "Resume with a representable credential format.")
			}
		}
		index := len(snapshot.entries)
		record := requestSecretRecord{source: entry, kind: kind, user: user, aliases: []string{entry.Name}}
		if old, ok := byIdentity[entry.Identity]; ok {
			if !sameRequestSecretIdentity(snapshot.entries[old], record) {
				return nil, memberError("bad_secret", "derived request identity has conflicting current variants", "Rebuild the session's canonical secret generation.")
			}
			aliases := snapshot.entries[old].aliases
			if aliases[len(aliases)-1] != entry.Name {
				snapshot.entries[old].aliases = append(aliases, entry.Name)
			}
			continue
		}
		byIdentity[entry.Identity] = index
		snapshot.entries = append(snapshot.entries, record)
		snapshot.credentialHeaders[binding.Header] = true
		for _, authority := range binding.Hosts {
			snapshot.authorities[authority] = append(snapshot.authorities[authority], index)
		}
	}
	return snapshot.withTripwireSentinels(nil)
}

// Backend supplies public stand-ins only, never historical real values. This
// once-per-generation view shares immutable eligibility indexes and owns only
// its public tripwire union. Extras cannot become request variants or names.
func (s *requestSecretSnapshot) withTripwireSentinels(values []api.Secret) (*requestSecretSnapshot, error) {
	if s == nil {
		return nil, memberError("bad_secret", "request tripwire view has no canonical generation", "Rebuild the session's canonical stand-in view.")
	}
	seen := make(map[api.Secret]struct{}, len(s.tripwires)+len(s.entries)+len(values))
	add := func(value api.Secret) error {
		if value == "" || strings.ContainsRune(string(value), 0) {
			return memberError("bad_secret", "invalid public stand-in in request tripwire view", "Rebuild the session's canonical stand-in view.")
		}
		seen[value] = struct{}{}
		return nil
	}
	for _, value := range s.tripwires {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	for _, entry := range s.entries {
		if err := add(entry.source.Sentinel); err != nil {
			return nil, err
		}
	}
	for _, value := range values {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	output := *s
	output.tripwires = make([]api.Secret, 0, len(seen))
	for value := range seen {
		output.tripwires = append(output.tripwires, value)
	}
	sort.Slice(output.tripwires, func(i, j int) bool { return output.tripwires[i] < output.tripwires[j] })
	return &output, nil
}

func requestCredentialValue(kind, user string, value api.Secret) (api.Secret, bool) {
	var result string
	switch kind {
	case "raw":
		result = string(value)
	case "bearer":
		padding := false
		characters := false
		for i := range len(value) {
			b := value[i]
			if b == '=' {
				padding = true
				continue
			}
			if padding || !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~+/", rune(b))) {
				return "", false
			}
			characters = true
		}
		if !characters {
			return "", false
		}
		result = "Bearer " + string(value)
	case "basic":
		result = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+string(value)))
	default:
		return "", false
	}
	if strings.Trim(result, " \t") != result {
		return "", false
	}
	for i := range len(result) {
		if result[i] < 0x20 && result[i] != '\t' || result[i] == 0x7f {
			return "", false
		}
	}
	return api.Secret(result), true
}
