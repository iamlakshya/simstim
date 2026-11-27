package egress

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func retiredPrimaryFixture(sentinel api.Secret, id byte) api.RetiredPrimarySecretState {
	return api.RetiredPrimarySecretState{Sentinel: sentinel, Identity: strings.Repeat(string(id), 32), Shape: &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 4, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}, Binding: api.SecretBind{Hosts: []string{"example.com"}, Header: "Authorization", Format: "Bearer {value}"}}
}

func TestBoundColdWinningRootShapeCreatesCurrentCAndRetiresOldChildAAndRootB(t *testing.T) {
	a, b := retiredPrimaryFixture("AAAA0000", 'a'), retiredPrimaryFixture("BBBB1111", 'b')
	args := boundRegistryArgs(map[string]string{"KEY": "CCCC2222"})
	args.Saved = map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: b.Sentinel, Shape: b.Shape, Identity: b.Identity, Armed: true}}
	args.SavedRetired = map[string][]api.RetiredPrimarySecretState{"KEY": {a}}
	server, result := newBoundRegistryServer(t, args)
	current := result.Secrets["KEY"]
	if current.Sentinel == a.Sentinel || current.Sentinel == b.Sentinel || len(result.Retired["KEY"]) != 2 || len(result.Minted) != 0 {
		t.Fatal("cold root shape winner discarded old public S, reused it or fabricated minted metadata")
	}
	request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer " + string(current.Sentinel)}})
	plan, failure := prepareRequestSecrets(server.currentMembers().requestView, request, raw, checked, "https")
	if failure != nil || len(plan.replacements) != 1 || plan.replacements[0].value != "Bearer CCCC2222" {
		t.Fatal("returned current C was not the sole injection identity")
	}
	for _, old := range []api.Secret{a.Sentinel, b.Sentinel} {
		request, raw, checked = requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer " + string(old)}})
		if plan, failure := prepareRequestSecrets(server.currentMembers().requestView, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden || len(plan.replacements) != 0 {
			t.Fatal("retired primary obtained armed or unarmed request eligibility")
		}
		request, raw, checked = requestSecretFixture(t, "https", "example.com", "/v1?old="+string(old), nil)
		if failure := checkRequestSecretTripwires(server.currentMembers().requestView, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden {
			t.Fatal("an old known public S lost its global tripwire")
		}
		if server.scrubber.ScrubText(string(old)) != "[KEY]" {
			t.Fatal("retired public S lost its original configured audit name")
		}
	}
	if err := ValidateBoundInitialResult(t.Context(), args, result); err != nil {
		t.Fatal(err)
	}
	args.Initial.Secrets = nil
	args.SourceKinds = nil
	args.Saved = result.Secrets
	args.SavedRetired = api.CloneRetiredPrimaryStates(result.Retired)
	args.SavedRetired["KEY"] = append(args.SavedRetired["KEY"], args.SavedRetired["KEY"]...)
	_, next := newBoundRegistryServer(t, args)
	if next.Secrets["KEY"].Armed || next.Secrets["KEY"].Sentinel == current.Sentinel || len(next.Retired["KEY"]) != 3 {
		t.Fatal("repeated boot reset known public history, revived values or grew duplicate retired records")
	}
	if err := ValidateBoundInitialResult(t.Context(), args, next); err != nil {
		t.Fatal(err)
	}
}

func TestBoundRetiredMetadataRejectsForgedClassBindingAndAmbiguousIdentity(t *testing.T) {
	for _, mutate := range []func(*api.RetiredPrimarySecretState){
		func(s *api.RetiredPrimarySecretState) { s.Shape.Version = 99 },
		func(s *api.RetiredPrimarySecretState) {
			s.Shape.Slots[0].Class = "ascii"
			s.Shape.Slots[0].ASCII = "AB"
		},
		func(s *api.RetiredPrimarySecretState) { s.Binding.Header = "Host" },
		func(s *api.RetiredPrimarySecretState) { s.Identity = "forged" },
		func(s *api.RetiredPrimarySecretState) { s.Shape.Prefix = "AA" },
	} {
		public := retiredPrimaryFixture("AAAA0000", 'a')
		mutate(&public)
		if err := ValidateRetiredPrimarySecretState(t.Context(), "KEY", public); err == nil {
			t.Fatal("invalid historical public metadata passed the canonical validator")
		}
	}
	args := boundRegistryArgs(map[string]string{"KEY": "CCCC2222"})
	root := retiredPrimaryFixture("BBBB1111", 'b')
	args.Saved = map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: root.Sentinel, Shape: root.Shape, Identity: root.Identity}}
	conflict := root
	conflict.Identity = strings.Repeat("a", 32)
	args.SavedRetired = map[string][]api.RetiredPrimarySecretState{"KEY": {conflict}}
	if _, err := prepareBoundInitial(t.Context(), args); err == nil {
		t.Fatal("conflicting snapshot identity silently retired over the current root metadata")
	}
}

func TestBoundAllRetiredPublicRecordsCountAgainstExistingFrameLimit(t *testing.T) {
	args := boundRegistryArgs(nil)
	args.SavedRetired = make(map[string][]api.RetiredPrimarySecretState)
	for i := range 512 {
		public := retiredPrimaryFixture(api.Secret(strings.Repeat("A", 8192)), 'a')
		public.Identity = fmt.Sprintf("%032x", i+1)
		public.Shape.Slots = []api.SecretShapeSlot{{Class: "ascii-upper", Count: 8192, Width: 1}}
		args.SavedRetired["KEY"] = append(args.SavedRetired["KEY"], public)
	}
	requireBoundError(t, checkBoundInitialFrame(args), "state_too_large")
	if _, err := prepareBoundInitial(t.Context(), args); err == nil {
		t.Fatal("oversized known public history reached boot publication")
	}
}
