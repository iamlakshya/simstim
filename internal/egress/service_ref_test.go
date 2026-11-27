package egress

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func TestServiceAtomicEndpointGrantIsNotHiddenByBroadPermanentNetworking(t *testing.T) {
	path := "/repos/org/repo/contents/file"
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.github.com" + path, Once: true, GrantID: "endpoint"}
	broad := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.github.com"}
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{broad, grant}})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "API.GITHUB.COM.", Port: 443, Path: path, Method: "PUT"}
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.admitServiceRequest(request, permit, true, evalReserve).Allow || len(permit.owned) != 1 {
		t.Fatal("broad permanent host permission hid the required once endpoint grant")
	}
	for slot := range permit.owned {
		if slot.key.grantID != "endpoint" {
			t.Fatal("service admission spent an unrelated lease")
		}
	}
	permit.Commit()
	service := &serviceRequest{request: request, pathGrant: true}
	if !service.allowed(engine, permit) {
		t.Fatal("service request lost its original committed path lease")
	}
	revoked, err := NewEngine("shell", api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{broad}})
	if err != nil {
		t.Fatal(err)
	}
	engine.ledger.syncPolicy(revoked)
	if !revoked.evaluateHTTP(request, permit, evalRevalidate).Allow {
		t.Fatal("fixture lost its broad host permission")
	}
	if service.allowed(revoked, permit) {
		t.Fatal("removed endpoint authority fell back to a broad host on its live request")
	}
}

func TestServiceAtomicPlanReusesRequiredPathOnceAndSharesDeniesExpiry(t *testing.T) {
	path := "/user/repos"
	for _, blocked := range []string{"none", "deny", "lock", "expired"} {
		t.Run(blocked, func(t *testing.T) {
			now := time.Now()
			pathGrant := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.github.com" + path, Once: true, GrantID: "required"}
			policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.github.com", Once: true, GrantID: "unrelated"}, pathGrant}}
			switch blocked {
			case "deny":
				policy.Rules = append(policy.Rules, api.Rule{Layer: api.LayerSession, Target: "api.github.com" + path})
			case "lock":
				policy.Locks = []api.Lock{{Layer: api.LayerAll, Deny: []string{"api.github.com" + path}}}
			case "expired":
				policy.Rules[1].Until = now
			}
			engine, err := NewEngine("shell", policy)
			if err != nil {
				t.Fatal(err)
			}
			engine.now = func() time.Time { return now }
			permit := engine.NewPermit()
			defer permit.Close()
			before := snapshotDNSLedger(engine)
			decision := engine.admitServiceRequest(Request{Host: "api.github.com", Port: 443, Method: "POST", Path: path}, permit, true, evalReserve)
			if blocked == "none" {
				if !decision.Allow || len(permit.owned) != 1 {
					t.Fatal("required endpoint lease was not reused for exact networking")
				}
				for slot := range permit.owned {
					if slot.key.grantID != "required" {
						t.Fatal("required path spent an unrelated once grant")
					}
				}
			} else if decision.Allow || len(permit.owned) != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
				t.Fatal("blocked service plan mutated a lease before complete admission")
			}
		})
	}
}

func TestRESTRefDefaultsAndListedMergeOperationsRefuseBeforeOrigin(t *testing.T) {
	for _, test := range []struct{ method, path, body string }{
		{"PUT", "/repos/org/repo/contents/file", `{"message":"authored","content":"YQ=="}`},
		{"PUT", "/repos/org/repo/contents/file", `{"message":"authored","content":"YQ==","branch":"main"}`},
		{"POST", "/repos/org/repo/git/refs", `{"ref":"refs/heads/main","sha":"authored"}`},
		{"PATCH", "/repos/org/repo/git/refs/heads/main", `{"sha":"authored","force":true}`},
		{"DELETE", "/repos/org/repo/git/refs/heads/main", ``},
		{"POST", "/repos/org/repo/merges", `{"base":"main","head":"authored"}`},
		{"PUT", "/repos/org/repo/pulls/1/merge", `{}`},
		{"PUT", "/repos/org/repo/pulls/1/merge-async", `{"merge_action":"merge_queue","bypass_rules":false}`},
		{"POST", "/repos/org/repo/merge-upstream", `{"branch":"main"}`},
		{"POST", "/repos/org/repo/branches/name/with/slash/rename", `{"new_name":"main"}`},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetBalanced}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			response, audit := f.request(t, test.method, "api.github.com", test.path, []byte(test.body), "")
			if response.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 || !strings.HasPrefix(audit.Rule, "service:") {
				t.Fatal("default ref-moving service operation reached an origin")
			}
		})
	}
}

func TestRESTOwnNamespaceAndLegacyEndpointExceptionRemainSeparateFromPushDoor(t *testing.T) {
	prefix := brand.Name + "/model234567/"
	for _, test := range []struct {
		method, path, body string
		workflow, explicit bool
	}{
		{"PUT", "/repos/org/public-repo/contents/file", `{"message":"authored","content":"YQ==","branch":"` + prefix + `fix"}`, false, false},
		{"POST", "/repos/org/repo/git/refs", `{"ref":"refs/heads/` + prefix + `fix","sha":"authored"}`, false, false},
		{"PATCH", "/repos/org/repo/git/refs/heads/" + prefix + "fix", `{"sha":"authored"}`, false, false},
		{"DELETE", "/repos/org/repo/git/refs/heads/" + prefix + "fix", ``, false, false},
		{"PUT", "/repos/org/repo/contents/.github/workflows/build.yml", `{"message":"authored","content":"YQ==","branch":"` + prefix + `fix"}`, true, false},
		{"PUT", "/repos/org/repo/contents/.github/workflows/build.yml", `{"message":"authored","content":"YQ==","branch":"main"}`, true, true},
		{"PUT", "/repos/org/repo/pulls/1/merge-async", `{"merge_action":"direct_merge","merge_method":"squash"}`, false, true},
	} {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			policy := api.Policy{Preset: api.PresetBalanced}
			if test.explicit {
				policy.Rules = []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.github.com" + test.path}}
			} // Legacy nil Methods is deliberate.
			f := newModelHTTPFixture(t, "shell", policy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != test.body {
					http.Error(w, "bytes changed", 500)
					return
				}
				w.WriteHeader(200)
			}))
			response, _ := f.request(t, test.method, "api.github.com", test.path, []byte(test.body), "")
			want := 200
			if test.workflow && !test.explicit {
				want = 403
			}
			if response.Code != want {
				t.Fatal("namespace, workflow and explicit legacy endpoint contracts did not compose")
			}
		})
	}
	// The public-repo spelling above is an authored scope fixture. No forge
	// metadata is queried here: universal API publishing consent remains the
	// explicit Root-owned question9c1c1f33, not a claimed protection.
}

func TestRESTUnknownShapeNeverSpendsTheEndpointLease(t *testing.T) {
	path := "/repos/org/repo/contents/file"
	policy := api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.github.com"}, {Layer: api.LayerSession, Allow: true, Target: "api.github.com" + path, Once: true, GrantID: "endpoint"}}}
	f := newModelHTTPFixture(t, "shell", policy, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	for _, body := range []string{`{"message":"authored","content":"YQ==","branch":1}`, `{"message":"authored","content":"YQ==","Branch":"main"}`, `{"message":"authored","content":"YQ==","branch":"main","branch":"other"}`, `{"message":"authored","content":"YQ==","branch":"main","unknown_target":"other"}`} {
		before := snapshotDNSLedger(f.env.srv.currentEngine())
		req := httptest.NewRequest("PUT", "http://api.github.com:443"+path, strings.NewReader(body)).WithContext(t.Context())
		req.Body = &decoyLedgerProbeBody{t: t, engine: f.env.srv.currentEngine(), reader: strings.NewReader(body)}
		writer := httptest.NewRecorder()
		f.env.srv.proxyHTTPRequest(writer, req, StreamHeader{Via: "explicit", Host: "api.github.com", Port: 443}, "http", nil)
		if writer.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(f.env.srv.currentEngine())) {
			t.Fatal("unknown body shape reserved or reached an origin despite an endpoint grant")
		}
	}
	valid := []byte(`{"message":"authored","content":"YQ==","branch":"main"}`)
	response, _ := f.request(t, "PUT", "api.github.com", path, valid, "")
	if response.Code != 200 || f.requests.Load() != 1 {
		t.Fatal("shape failures consumed the sole exact endpoint grant")
	}
	response, _ = f.request(t, "PUT", "api.github.com", path, bytes.Clone(valid), "")
	if response.Code != 403 || f.requests.Load() != 1 {
		t.Fatal("a second outside-prefix request reused the spent endpoint grant")
	}
}
