package egress

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestGitHubProtectedOperationsNeedAPathBeforeAnyOriginIO(t *testing.T) {
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/user/repos", `{"name":"authored"}`},
		{"POST", "/USER/repos/", `{"name":"authored"}`},
		{"POST", "/orgs/org/repos", `{"name":"authored"}`},
		{"POST", "/gists", `{"files":{}}`},
		{"PATCH", "/repos/org/repo", `{"private":false}`},
		{"PATCH", "/repos/org/repo", `{"visibility":"public"}`},
		{"PATCH", "/repos/org/repo", `{"private":false,"Private":true}`},
		{"PUT", "/repos/org/repo/contents/.github/workflows/build.yml", `{"content":"authored"}`},
		{"POST", "/repos/org/repo/actions/runners/registration-token", `{}`},
		{"PUT", "/repos/org/repo/collaborators/user", `{}`},
	} {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetBalanced}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			response, audit := f.request(t, test.method, "api.github.com", test.path, []byte(test.body), "")
			if response.Code != http.StatusForbidden || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 || audit.Decision != "deny" || !strings.HasPrefix(audit.Rule, "service:") {
				t.Fatalf("protected service reached origin: status=%d audit=%+v", response.Code, audit)
			}
		})
	}
}

func TestGitHubBenignMetadataPreservesBytesAndExplicitPathGrantsWork(t *testing.T) {
	const benign = " {\"description\": \"kept\", \"topics\": [\"private\"]} "
	f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetBalanced}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != benign {
			http.Error(w, "metadata changed", 500)
			return
		}
		w.WriteHeader(200)
	}))
	response, _ := f.request(t, "PATCH", "api.github.com", "/repos/org/repo", []byte(benign), "")
	if response.Code != 200 || f.requests.Load() != 1 {
		t.Fatalf("benign repository metadata was changed or refused: %d", response.Code)
	}
	post := []string{"POST"}
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.github.com/user/repos", Methods: &post}
	g := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	created, _ := g.request(t, "POST", "api.github.com", "/user/repos", []byte(`{"name":"authored"}`), "")
	if created.Code != http.StatusCreated || g.requests.Load() != 1 {
		t.Fatal("an exact method/path grant did not open its service operation")
	}
}

func TestGitHubLivePathRemovalDoesNotFallBackToBalancedHostPermission(t *testing.T) {
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.github.com/user/repos"}
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{grant}})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "api.github.com", Port: 443, Method: "POST", Path: "/user/repos"}
	permit := engine.NewPermit()
	if !engine.evaluateHTTP(request, permit, evalReserve).Allow {
		t.Fatal("initial network allowance missing")
	}
	permit.Commit()
	service := &serviceRequest{request: request, pathGrant: true}
	if !service.allowed(engine, permit) {
		t.Fatal("initial exact service allowance missing")
	}
	revoked, err := NewEngine("shell", api.Policy{Preset: api.PresetBalanced})
	if err != nil {
		t.Fatal(err)
	}
	engine.ledger.syncPolicy(revoked)
	if !revoked.evaluateHTTP(request, permit, evalRevalidate).Allow {
		t.Fatal("test did not retain ordinary balanced host permission")
	}
	if service.allowed(revoked, permit) {
		t.Fatal("removed service path grant fell back to a broad host allowance")
	}
}
