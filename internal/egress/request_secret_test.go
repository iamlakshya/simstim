package egress

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func requestSecretFixture(t *testing.T, scheme, authority, target string, headers http.Header) (*http.Request, requestSecretRawView, Request) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, scheme+"://"+authority+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	original := requestSecretRawView{Target: request.URL.RequestURI(), Authority: request.Host, Headers: request.Header.Clone()}
	original.Headers["Host"] = []string{request.Host}
	port := 443
	if scheme == "http" {
		port = 80
	}
	host, port, err := parseHostHeader(authority, port)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := canonicalHTTPRequest(request, StreamHeader{Host: host, Port: port}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	return request, original, checked
}

func requestSecretTestSnapshot(t *testing.T, header, format string, value, sentinel api.Secret, armed bool) *requestSecretSnapshot {
	t.Helper()
	snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{{Name: "API_TOKEN", Identity: "fixture-primary", Armed: armed, Binding: api.SecretBind{Hosts: []string{"api.example.com"}, Header: header, Format: format}, Value: value, Sentinel: sentinel}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRequestSecretPlanSwapsOnlyTheNamedHeaderAndPreservesItsGeneration(t *testing.T) {
	for _, tc := range []struct {
		header, format, input string
		value, sentinel       api.Secret
	}{
		{"X-Api-Key", "{value}", "fake-key-000000", "real-key-000000", "fake-key-000000"},
		{"Authorization", "Bearer {value}", "bEaReR fake-token-12345", "real-token-12345", "fake-token-12345"},
		{"Authorization", "basic:fixed:{value}", "bAsIc " + base64.StdEncoding.EncodeToString([]byte("fixed:fake:password")), "real:password", "fake:password"},
	} {
		snapshot := requestSecretTestSnapshot(t, tc.header, tc.format, tc.value, tc.sentinel, true)
		headers := http.Header{tc.header: []string{tc.input}, "Anthropic-Beta": []string{"oauth-2025-04-20"}, "X-Unrelated": []string{"kept"}}
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1?q=kept", headers)
		checked.Now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // policy clock is not a routing/header identity
		beforeHeaders := request.Header.Clone()
		beforeURL, beforeHost := request.URL.String(), request.Host
		plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
		if failure != nil || plan.generation != snapshot || len(plan.replacements) != 1 {
			t.Fatal("allowed named-header plan was absent or lost its generation")
		}
		replacement := plan.replacements[0]
		if replacement.name != "API_TOKEN" || replacement.header != tc.header || len(replacement.removeKeys) != 1 || replacement.removeKeys[0] != tc.header {
			t.Fatal("named-header replacement or alias-removal identity changed")
		}
		want, _ := requestCredentialValue(snapshot.entries[0].kind, snapshot.entries[0].user, tc.value)
		if replacement.value != want || !reflect.DeepEqual(request.Header, beforeHeaders) || request.URL.String() != beforeURL || request.Host != beforeHost {
			t.Fatal("pure credential plan mutated request routing or unrelated headers")
		}
		if snapshot.entries[0].kind == "basic" {
			view := &http.Request{Header: http.Header{"Authorization": []string{string(replacement.value)}}}
			user, password, ok := view.BasicAuth()
			if !ok || user != "fixed" || password != string(tc.value) {
				t.Fatal("Basic plan changed the fixed username or failed password-only substitution")
			}
		}
	}
}

func TestRequestSecretPinRejectsOtherAndDuplicateCredentialForms(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "Authorization", "Bearer {value}", "real-token-12345", "fake-token-12345", true)
	for _, headers := range []http.Header{
		{"Authorization": []string{"Bearer another-credential"}},
		{"Authorization": []string{"Bearer real-token-12345"}},
		{"Authorization": []string{"Bearer fake-token-12345", "Bearer fake-token-12345"}},
		{"Authorization": []string{"Bearer fake-token-12345"}, "authorization": []string{"Bearer fake-token-12345"}},
		{"Authorization": []string{"Bearer fake-token-12345"}, "authorization": nil},
		{"Authorization": nil},
		{"X-Api-Key": []string{"attacker-key"}},
		{"Api-Key": []string{"attacker-key"}},
		{"Proxy-Authorization": []string{"Basic dXNlcjpwYXNz"}},
		{"Authorization": []string{"Bearer\tfake-token-12345"}},
		{"Authorization": []string{"Bearer fake-token-12345 extra"}},
	} {
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", headers)
		before := request.Header.Clone()
		plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
		if failure == nil || failure.status != http.StatusForbidden || failure.detail.Code != "secret_misuse" || failure.detail.Hint == "" || len(plan.replacements) != 0 || !reflect.DeepEqual(before, request.Header) {
			t.Fatal("credential misuse produced a plan, wrong refusal or request mutation")
		}
	}
}

func TestRequestSecretAbsenceUnarmedAndUnknownProtocolScope(t *testing.T) {
	for _, armed := range []bool{false, true} {
		value := api.Secret("")
		if armed {
			value = "real-token-12345"
		}
		snapshot := requestSecretTestSnapshot(t, "Authorization", "Bearer {value}", value, "fake-token-12345", armed)
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/public", http.Header{"Cookie": []string{"session=ordinary"}, "X-Custom-Auth": []string{"not-in-config"}})
		plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
		if armed {
			if failure != nil || len(plan.replacements) != 0 {
				t.Fatal("absent named credentials were inserted or unconfigured protocols were claimed")
			}
		} else if failure == nil || failure.status != http.StatusUnauthorized || failure.detail.Code != "secret_unarmed" || failure.detail.Hint == "" {
			t.Fatal("otherwise-valid unarmed bound request did not get an attach hint")
		}
		request, raw, checked = requestSecretFixture(t, "https", "api.example.com", "/public", http.Header{"X-Api-Key": []string{"other"}})
		if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden {
			t.Fatal("unarmed response hid prior credential misuse")
		}
	}
}

func TestRequestSecretLowercaseSingleAliasHasOneCanonicalReplacement(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "Authorization", "Bearer {value}", "real-token-12345", "fake-token-12345", true)
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"authorization": []string{"Bearer fake-token-12345"}})
	plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
	if failure != nil || len(plan.replacements) != 1 || len(plan.replacements[0].removeKeys) != 1 || plan.replacements[0].removeKeys[0] != "authorization" || plan.replacements[0].header != "Authorization" {
		t.Fatal("lowercase input alias would survive beside the real credential")
	}
}

func TestRequestSecretSnapshotOwnsBindingsAndRequiresExactRawCanonicalAgreement(t *testing.T) {
	hosts := []string{"api.example.com"}
	snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{{Name: "API_TOKEN", Identity: "fixture-primary", Armed: true, Binding: api.SecretBind{Hosts: hosts, Header: "X-Api-Key", Format: "{value}"}, Value: "real-key-000000", Sentinel: "fake-key-000000"}})
	if err != nil {
		t.Fatal(err)
	}
	hosts[0] = "other.example.com"
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
	if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure != nil || len(plan.replacements) != 1 {
		t.Fatal("caller mutated a published binding slice")
	}
	raw.Headers["X-Api-Key"] = []string{"other"}
	if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
		t.Fatal("raw credential mismatch was silently discarded by canonicalisation")
	}
}

type requestSecretBodyProbe struct{ reads int }

func (p *requestSecretBodyProbe) Read([]byte) (int, error) { p.reads++; return 0, io.EOF }
func (*requestSecretBodyProbe) Close() error               { return nil }

func TestRequestSecretPlanDoesNotReadBodyOrReplaceTrailers(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", "real-key-000000", "fake-key-000000", true)
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
	body := new(requestSecretBodyProbe)
	request.Body = body
	request.Trailer = http.Header{"X-Checksum": []string{"ordinary"}}
	trailers := request.Trailer.Clone()
	if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure != nil || body.reads != 0 || !reflect.DeepEqual(trailers, request.Trailer) {
		t.Fatal("header preparation consumed body/trailer or changed fields")
	}
	for _, fields := range []http.Header{{"X-Api-Key": []string{"fake-key-000000"}}, {"X-Checksum": []string{"fake-key-000000"}}} {
		if failure := snapshot.checkRequestSecretTrailers(fields, "api.example.com:443"); failure == nil || failure.status != http.StatusForbidden {
			t.Fatal("late request trailer escaped its pure no-substitution guard")
		}
	}
	request.Trailer = http.Header{"X-Api-Key": nil}
	if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 || body.reads != 0 {
		t.Fatal("declared credential trailer survived the pre-origin-write plan")
	}
}

func TestRequestSecretDerivedTypesRedactEveryVerbAndValueForm(t *testing.T) {
	const marker = "request-private-marker-123"
	snapshot := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", api.Secret(strings.Repeat("x", len(marker))), marker, true)
	entry := snapshot.entries[0].source
	record := snapshot.entries[0]
	raw := requestSecretRawView{Target: marker, Authority: marker, Headers: http.Header{"X-Private": []string{marker}}}
	replacement := requestSecretReplacement{name: "API_TOKEN", header: "X-Api-Key", value: marker}
	plan := requestSecretPlan{generation: snapshot, replacements: []requestSecretReplacement{replacement}}
	bag := requestSecretHeaderBag{values: map[string][]string{"X": []string{marker}}}
	for _, value := range []any{entry, &entry, record, &record, *snapshot, snapshot, raw, &raw, replacement, &replacement, plan, &plan, bag, &bag} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			if fmt.Sprintf(verb, value) != api.Redacted {
				t.Fatal("a derived request value bypassed explicit redacting Format")
			}
		}
	}
}

func TestRequestSecretExactAliasesShareAPlanButConflictingValuesRefuse(t *testing.T) {
	binding := api.SecretBind{Hosts: []string{"api.example.com"}, Header: "X-Api-Key", Format: "{value}"}
	for _, conflict := range []bool{false, true} {
		second := api.Secret("real-key-000000")
		if conflict {
			second = "evil-key-000000"
		}
		snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{{Name: "FIRST", Identity: "canonical-alias", Armed: true, Binding: binding, Value: "real-key-000000", Sentinel: "fake-key-000000"}, {Name: "SECOND", Identity: "canonical-alias", Armed: true, Binding: binding, Value: second, Sentinel: "fake-key-000000"}})
		if conflict {
			if err == nil {
				t.Fatal("conflicting alias current values entered one canonical identity")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
		plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
		if failure != nil || len(plan.replacements) != 1 || plan.replacements[0].name != "FIRST" || !reflect.DeepEqual(plan.usedNames(), []string{"FIRST", "SECOND"}) || !reflect.DeepEqual(plan.usedIdentities(), []string{"canonical-alias"}) {
			t.Fatal("canonical identical aliases did not share a deterministic named plan")
		}
	}
}
