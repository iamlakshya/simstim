package egress

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRequestSecretWrongDestinationAndEncodedTripwiresRefuseBeforeAPlan(t *testing.T) {
	const sentinel = "fake-key-000000"
	snapshot := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", "real-key-000000", sentinel, true)
	for _, tc := range []struct {
		scheme, authority, target string
		headers                   http.Header
	}{
		{"https", "other.example.com", "/v1", http.Header{"X-Api-Key": []string{sentinel}}},
		{"http", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{sentinel}}},
		{"https", "api.example.com:8443", "/v1", http.Header{"X-Api-Key": []string{sentinel}}},
		{"https", "api.example.com", "/v1", http.Header{"X-Other": []string{sentinel}}},
		{"https", "other.example.com", "/v1?copy=%66ake-key-000000", nil},
		{"https", "other.example.com", "/v1?copy=%2566ake-key-000000", nil},
		{"https", "other.example.com", "/v1?bad=%ZZ&copy=%2566ake-key-000000", nil},
		{"https", "other.example.com", "/v1", http.Header{"X-Record": []string{"bad=%ZZ;%2566ake-key-000000"}}},
		{"https", "other.example.com", "/v1", http.Header{"X-" + strings.ToUpper(sentinel): []string{"ordinary"}}},
		{"https", "other.example.com", "/v1", http.Header{"X-%2566ake-key-000000": []string{"ordinary"}}},
		{"https", "other.example.com", "/v1", http.Header{"X-Record": []string{"Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+sentinel))}}},
		{"https", "other.example.com", "/v1", http.Header{"X-Record": []string{"%42asic " + base64.StdEncoding.EncodeToString([]byte("user:"+sentinel))}}},
		{"https", "other.example.com", "/v1", http.Header{"X-Record": []string{"Basic " + base64.StdEncoding.EncodeToString([]byte("user:%66ake-key-000000"))}}},
	} {
		request, raw, checked := requestSecretFixture(t, tc.scheme, tc.authority, tc.target, tc.headers)
		plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, tc.scheme)
		if failure == nil || len(plan.replacements) != 0 || failure.detail.Hint == "" {
			t.Fatal("encoded or wrong-destination stand-in acquired a real-value plan")
		}
		if strings.Contains(failure.detail.Error(), sentinel) || strings.Contains(failure.detail.Error(), "real-key") {
			t.Fatal("tripwire error exposed a field or value")
		}
	}
}

func TestRequestSecretBasicRequiresFixedUserCanonicalEncodingAndPasswordOnly(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "Authorization", "basic:fixed:{value}", "real:password", "fake:password", true)
	canonical := base64.StdEncoding.EncodeToString([]byte("fixed:fake:password"))
	for _, value := range []string{
		"Basic " + base64.StdEncoding.EncodeToString([]byte("other:fake:password")),
		"Basic " + base64.StdEncoding.EncodeToString([]byte("fixed:other-password")),
		"Basic " + strings.TrimRight(canonical, "="),
		"Basic " + canonical + " ",
		"Basic  " + canonical,
		"Basic YTp=",
		"Basic not-base64",
	} {
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"Authorization": []string{value}})
		if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
			t.Fatal("noncanonical Basic or changed identity reached a real-value plan")
		}
	}
	if _, _, ok := requestSecretBasic("Basic YTp="); ok {
		t.Fatal("nonzero base64 pad bits were accepted")
	}
	if _, _, ok := requestSecretBasic("Basic " + base64.URLEncoding.EncodeToString([]byte("fixed:\xff"))); ok {
		t.Fatal("URL-safe encoding acquired Basic authority")
	}
	if _, password, ok := requestSecretBasic("bAsIc " + base64.StdEncoding.EncodeToString([]byte("fixed:one:two"))); !ok || password != "one:two" {
		t.Fatal("standard first-colon/password semantics changed")
	}
}

func TestRequestSecretMalformedRoutingAndRawAliasHostStayRefused(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", "real-key-000000", "fake-key-000000", true)
	for _, change := range []func(*http.Request, *requestSecretRawView, *Request){
		func(_ *http.Request, raw *requestSecretRawView, _ *Request) { raw.Authority = "other.example.com" },
		func(_ *http.Request, raw *requestSecretRawView, _ *Request) {
			raw.Headers["host"] = []string{"other.example.com"}
		},
		func(request *http.Request, _ *requestSecretRawView, _ *Request) {
			request.URL.Host = "other.example.com"
		},
		func(request *http.Request, _ *requestSecretRawView, _ *Request) { request.Host = "other.example.com" },
		func(_ *http.Request, _ *requestSecretRawView, checked *Request) { checked.Path = "/different" },
		func(_ *http.Request, raw *requestSecretRawView, _ *Request) {
			raw.Target = "https://user:password@api.example.com/v1"
		},
		func(_ *http.Request, raw *requestSecretRawView, _ *Request) { raw.Target = "http://api.example.com/v1" },
	} {
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
		change(request, &raw, &checked)
		if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || failure.status != http.StatusBadRequest || len(plan.replacements) != 0 {
			t.Fatal("routing inconsistency reached a credential plan")
		}
	}
}

func TestRequestSecretExactAuthorisedTokenDominatesItsNestedStandin(t *testing.T) {
	snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{
		{Name: "OUTER", Identity: "outer-current", Armed: true, Binding: api.SecretBind{Hosts: []string{"api.example.com"}, Header: "X-Api-Key", Format: "{value}"}, Value: "real-overlap-ABC", Sentinel: "fake-overlap-ABC"},
		{Name: "INNER", Identity: "inner-current", Armed: true, Binding: api.SecretBind{Hosts: []string{"other.example.com"}, Header: "X-Other-Credential", Format: "{value}"}, Value: "XYZ", Sentinel: "ABC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-overlap-ABC"}})
	if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure != nil || len(plan.replacements) != 1 || plan.replacements[0].name != "OUTER" {
		t.Fatal("a separately approved exact credential identity was relabelled by a nested stand-in")
	}
	request, raw, checked = requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Other": []string{"ABC"}})
	if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil {
		t.Fatal("nested protection became a general header exemption")
	}
}

func TestRequestSecretProjectionLimitsFailClosedAndKeepNormalPercentData(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", "real-key-000000", "fake-key-000000", true)
	request, raw, checked := requestSecretFixture(t, "https", "other.example.com", "/v1?q="+url.QueryEscape("ordinary%value"), http.Header{"X-Rate": []string{"50% ordinary"}})
	if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure != nil {
		t.Fatal("opaque malformed percent text gained an unrelated restriction")
	}
	deep := "%66ake-key-000000"
	for range maxScrubDecodePasses + 1 {
		deep = strings.ReplaceAll(deep, "%", "%25")
	}
	request, raw, checked = requestSecretFixture(t, "https", "other.example.com", "/v1?copy="+deep, nil)
	if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
		t.Fatal("residual encoded input was forwarded after the decode bound")
	}
	request, raw, checked = requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Large": []string{strings.Repeat("x", maxHTTPHeaderBytes)}})
	if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || failure.status != http.StatusBadRequest {
		t.Fatal("header projection exceeded the existing header bound")
	}
}

func TestRequestSecretQueryPlusDecodingCannotBeMaskedByMalformedPercent(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "Authorization", "basic:fixed:{value}", "real key", "fake key", true)
	for _, query := range []string{"copy=fake+key", "bad=%ZZ&copy=fake+key", "copy=fake%2520key"} {
		request, raw, checked := requestSecretFixture(t, "https", "other.example.com", "/v1?"+query, nil)
		if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
			t.Fatal("query projection lost a standard plus or nested percent-decoded stand-in")
		}
	}
}

func TestRequestSecretCanonicalMetadataStillOwnsDestinationAndHeaderAdmission(t *testing.T) {
	for _, binding := range []api.SecretBind{
		{Hosts: []string{"*.example.com"}, Header: "X-Api-Key", Format: "{value}"},
		{Hosts: []string{"api.example.com:8443"}, Header: "X-Api-Key", Format: "{value}"},
		{Hosts: []string{"https://api.example.com"}, Header: "X-Api-Key", Format: "{value}"},
		{Hosts: []string{"api.example.com/path"}, Header: "X-Api-Key", Format: "{value}"},
		{Hosts: []string{"api.example.com"}, Header: "Accept-Encoding", Format: "{value}"},
		{Hosts: []string{"api.example.com"}, Header: "Content-Encoding", Format: "{value}"},
		{Hosts: []string{"api.example.com"}, Header: "Content-Type", Format: "{value}"},
	} {
		if _, err := newRequestSecretSnapshot([]requestBoundSecret{{Name: "API_TOKEN", Identity: "fixture-primary", Armed: true, Binding: binding, Value: "real", Sentinel: "fake"}}); err == nil {
			t.Fatal("request view bypassed canonical metadata admission")
		}
	}
}
