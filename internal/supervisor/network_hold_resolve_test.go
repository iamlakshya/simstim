package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestResolvedNetworkSnapshotKeepsExactOriginalAction(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	args := api.NetworkHoldActionArgs{Version: 1, Session: "primary2", Request: strings.Repeat("a", 32), Scope: api.NetworkHoldSession, Revision: 7, Maximum: api.NetworkHoldExistingPolicy}
	valid := api.NetworkHoldResolution{Version: 1, Request: args.Request, Scope: args.Scope, Target: "api.example.com:443", Expires: now.Add(time.Minute), Revision: 7, Class: api.NetworkHoldPublicTarget}
	target, ok := validateResolvedNetworkSnapshotV1(valid, args, now)
	if !ok || target.Host != "api.example.com" || target.Port != 443 {
		t.Fatal("completed exact public snapshot representation refused")
	}
	for _, mutate := range []func(*api.NetworkHoldResolution){
		func(r *api.NetworkHoldResolution) { r.Request = strings.Repeat("b", 32) },
		func(r *api.NetworkHoldResolution) { r.Revision++ },
		func(r *api.NetworkHoldResolution) { r.Scope = api.NetworkHoldRepo },
		func(r *api.NetworkHoldResolution) { r.Class = api.NetworkHoldPrivateTarget },
		func(r *api.NetworkHoldResolution) { r.Expires = now },
		func(r *api.NetworkHoldResolution) { r.Target = "*.example.com:443" },
		func(r *api.NetworkHoldResolution) { r.Target = "api.example.com:443/path" },
		func(r *api.NetworkHoldResolution) { r.Target = "api.example.com" },
		func(r *api.NetworkHoldResolution) { r.Target = "api.example.com:443\n" },
		func(r *api.NetworkHoldResolution) { r.Target = "8.8.8.0/24:443" },
	} {
		changed := valid
		mutate(&changed)
		if _, ok := validateResolvedNetworkSnapshotV1(changed, args, now); ok {
			t.Fatal("changed/ambiguous actor snapshot accepted", changed)
		}
	}
	args.Scope = api.NetworkHoldOnce
	valid.Scope = args.Scope
	if _, ok := validateResolvedNetworkSnapshotV1(valid, args, now); ok {
		t.Fatal("once became a public policy snapshot")
	}
}

func TestResolvedNumericSnapshotCannotLosePublicRestriction(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	args := api.NetworkHoldActionArgs{Version: 1, Session: "primary2", Request: strings.Repeat("a", 32), Scope: api.NetworkHoldSession, Revision: 3, Maximum: api.NetworkHoldExistingPolicy}
	for _, value := range []string{"8.8.8.8:443", "[2606:4700:4700::1111]:443"} {
		resolved := api.NetworkHoldResolution{Version: 1, Request: args.Request, Scope: args.Scope, Target: value, Expires: now.Add(time.Minute), Revision: 3, Class: api.NetworkHoldPublicTarget}
		selected, ok := validateResolvedNetworkSnapshotV1(resolved, args, now)
		if !ok || !selected.Prefix.IsValid() || selected.Prefix.Bits() != selected.Prefix.Addr().BitLen() {
			t.Fatal("actual numeric representation lost its exact public-only branch", value)
		}
	}
}
