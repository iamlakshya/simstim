package egress

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPSecretRouteOrdersTripwirePolicyPinAndArmingWithoutSpendingOnce(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			pol := secretRoutePolicy(t, api.Rule{Allow: true, Target: secretRouteAuthority + "/allowed/once", Once: true, GrantID: "bound-request-instance"})
			args := secretRouteArgs(t, pol)
			seed := newSecretRouteFixture(t, protocol, 2, args, http.NotFoundHandler())
			args.Initial.Secrets = nil
			args.SourceKinds = nil // a cold boot supplies no fresh bound values
			args.Saved = api.ClonePublicSecretStates(seed.initial.Secrets)
			f := newSecretRouteFixture(t, protocol, 2, args, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/allowed/once" || req.Header.Get("Authorization") != "Bearer "+secretRouteReal {
					http.Error(w, "wrong actual origin credential", http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, secretRouteReal)
			}))
			if f.initial.Secrets[secretRouteName].Armed || f.sentinel() == seed.sentinel() {
				t.Fatal("actual cold restore was armed or reused the prior public stand-in")
			}
			for _, tc := range []struct {
				path, credential string
				status           int
				rule             string
			}{
				{"/denied", "", http.StatusForbidden, "policy"},
				{"/denied", "Bearer unrelated-credential", http.StatusForbidden, "policy"},
				{"/denied", "Basic not-base64", http.StatusForbidden, "policy"},
				{"/denied?standin=" + f.sentinel(), "", http.StatusForbidden, "secret_misuse"},
				{"/allowed/once", "Bearer unrelated-credential", http.StatusForbidden, "secret_misuse"},
				{"/allowed/once", "Basic not-base64", http.StatusBadRequest, "bad_request"},
				{"/allowed/once", "Bearer " + f.sentinel(), http.StatusUnauthorized, "secret_unarmed"},
			} {
				response := secretRouteRequest(t, f, http.MethodGet, tc.path, tc.credential)
				body := secretRouteRead(t, response)
				if response.StatusCode != tc.status || response.ProtoMajor != protocol {
					t.Fatalf("admission order returned %s %d instead of %d", response.Proto, response.StatusCode, tc.status)
				}
				path := strings.SplitN(tc.path, "?", 2)[0]
				entry := f.audit(t, http.MethodGet, path)
				if tc.rule == "policy" {
					if entry.Rule == "secret_unarmed" || entry.Rule == "secret_misuse" || entry.Decision != "deny" {
						t.Fatal("pin or arming ran before canonical policy")
					}
				} else if entry.Rule != tc.rule || entry.Status != tc.status {
					t.Fatal("actual audit did not preserve the refusal stage")
				}
				if strings.Contains(string(body), secretRouteReal) {
					t.Fatal("refusal exposed a real value")
				}
				f.requireNoOrigin(t)
			}
			var armed ArmResult
			f.call(t, CtlArm, ArmArgs{Version: MemberRegistryV2, Generation: f.initial.Generation, Values: map[string]api.Secret{secretRouteName: api.Secret(secretRouteReal)}}, &armed)
			if !armed.Secrets[secretRouteName].Armed || string(armed.Secrets[secretRouteName].Sentinel) != f.sentinel() {
				t.Fatal("compatible actual arm changed the in-generation stand-in")
			}
			response := secretRouteRequest(t, f, http.MethodGet, "/allowed/once", "Bearer "+f.sentinel())
			if body := secretRouteRead(t, response); response.StatusCode != http.StatusOK || string(body) != f.sentinel() {
				t.Fatal("prior refusals consumed once, or current real value escaped")
			}
			if entry := f.audit(t, http.MethodGet, "/allowed/once"); entry.Status != http.StatusOK || entry.Decision != "allow" {
				t.Fatal("first actual once request was not audited as allowed")
			}
			response = secretRouteRequest(t, f, http.MethodGet, "/allowed/once", "Bearer "+f.sentinel())
			_ = secretRouteRead(t, response)
			if response.StatusCode != http.StatusForbidden {
				t.Fatal("one request's committed grant authorised a second request")
			}
			if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
				t.Fatal("refusal or spent once reached an extra origin")
			}
		})
	}
}

func TestHTTPSecretRouteTripwiresAndCredentialPinsFailBeforeAnyOriginIO(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		for _, kind := range []string{"query", "encoded-query", "other-header", "header-name", "other-credential", "raw-real", "duplicate"} {
			t.Run(fmt.Sprintf("http%d_%s", protocol, kind), func(t *testing.T) {
				f := newSecretRouteFixture(t, protocol, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.NotFoundHandler())
				path := "/allowed/probe"
				if kind == "query" {
					path += "?value=" + f.sentinel()
				}
				if kind == "encoded-query" {
					path += "?value=" + secretRoutePercentTwice(f.sentinel())
				}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+secretRouteAuthority+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+f.sentinel())
				switch kind {
				case "other-header":
					req.Header.Set("X-Guest-Data", f.sentinel())
				case "header-name":
					req.Header.Set("X-"+f.sentinel(), "present")
				case "other-credential":
					req.Header.Set("X-Api-Key", "attacker-key")
				case "raw-real":
					req.Header.Set("Authorization", "Bearer "+secretRouteReal)
				case "duplicate":
					req.Header.Add("Authorization", "Bearer "+f.sentinel())
				}
				response, err := f.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = secretRouteRead(t, response)
				if response.StatusCode != http.StatusForbidden || response.ProtoMajor != protocol {
					t.Fatal("credential misuse reached an origin or changed guest protocol")
				}
				if entry := f.audit(t, http.MethodGet, "/allowed/probe"); entry.Rule != "secret_misuse" || entry.Decision != "deny" {
					t.Fatal("generic protocol or policy refusal hid a missing credential tripwire")
				}
				f.requireNoOrigin(t)
			})
		}
	}
}

func secretRoutePercentTwice(input string) string {
	var output strings.Builder
	for i := range len(input) {
		_, _ = fmt.Fprintf(&output, "%%25%02X", input[i])
	}
	return output.String()
}
