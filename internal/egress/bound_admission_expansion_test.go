package egress

import (
	"net/http"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundNewHeaderOnAlreadyBoundAuthorityJoinsEveryOldHTTPRequest(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"A": "ABCD0000"}))
	named := boundRequestScope(t, server, []string{"A"})
	anonymous := boundRequestScope(t, server, []string{})
	before := server.currentMembers()
	args := memberIntent(initial.Generation, map[string]api.Secret{"B": "WXYZ1111"}, "")
	args.Binds["B"] = api.SecretBind{Hosts: []string{"example.com"}, Header: "X-New-Credential", Format: "Bearer {value}"}
	done := make(chan error, 1)
	go func() { _, err := server.addBoundControlMember(memberJSON(t, args)); done <- err }()
	for _, request := range []*originRequest{named, anonymous} {
		select {
		case <-request.ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("new credential header skipped an old admitted request on the same authority")
		}
	}
	select {
	case <-done:
		t.Fatal("binding expansion published before physical request finalisers")
	default:
	}
	if server.currentMembers() != before {
		t.Fatal("new admission constraints became visible before old response owners joined")
	}
	named.close()
	anonymous.close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("joined old requests did not release binding expansion")
	}
}

func TestBoundEquivalentAliasDoesNotInterruptUnchangedAdmission(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"A": "ABCD0000"}))
	request := boundRequestScope(t, server, []string{"A"})
	result, err := server.addBoundControlMember(memberJSON(t, memberIntent(initial.Generation, map[string]api.Secret{"B": "ABCD0000"}, "")))
	if err != nil || request.ctx.Err() != nil || result.Env["B"] != initial.Secrets["A"].Sentinel || result.Secrets["B"].Identity != initial.Secrets["A"].Identity {
		t.Fatal("proven same-binding/value/shape/stand-in alias changed existing admission")
	}
}

func TestBoundRetiredOnlyAuthorityRequiresInspectionAndTripwiresWithoutInjection(t *testing.T) {
	retired := api.RetiredPrimarySecretState{Sentinel: "ABCD0000", Identity: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Shape: &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 4, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}, Binding: api.SecretBind{Hosts: []string{"example.com"}, Header: "Authorization", Format: "Bearer {value}"}}
	args := BoundInitArgs{Version: 2, Initial: InitArgs{SessionID: "bound234", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"EXPOSED": "exposed-only"}}, Modes: map[string]api.SecretMode{"EXPOSED": api.SecretModeExposed}, SavedRetired: map[string][]api.RetiredPrimarySecretState{"OLD": {retired}}}
	server, result := newBoundRegistryServer(t, args)
	if !server.needsSecretInspection("example.com", 443) || server.hasBoundDestination("example.com", 443) || len(result.Secrets) != 1 || server.currentEngine().Decide(Request{Host: "example.com", Port: 443}).Allow {
		t.Fatal("historical inspection metadata gained current binding, name or network authority")
	}
	request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer ABCD0000"}})
	plan, failure := prepareRequestSecrets(server.currentMembers().requestView, request, raw, checked, "https")
	if failure == nil || failure.status != http.StatusForbidden || len(plan.replacements) != 0 {
		t.Fatal("retired stand-in obtained a current header identity exemption")
	}
	server.policyMu.Lock()
	names := server.boundSecretNamesLocked("example.com:443")
	server.policyMu.Unlock()
	if len(names) != 1 || names[0] != "OLD" {
		t.Fatal("retired inspection scope lost its resource dependency")
	}
}
