package client

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHeldResolutionRetainsOnlyItsCompleteOriginalPublicResponse(t *testing.T) {
	c, r, args, _ := positiveHoldFixtureV1(t, api.NetworkHoldRepo, "")
	expires := time.Now().Add(time.Minute)
	var describes, resolves int
	sharedGrantWire(t, c, func(req proto.Request) any {
		switch req.Op {
		case api.OpPolicyDescribe:
			describes++
			return api.NetworkHoldDescriptor{Version: 1, Request: args.Request, Revision: 4, Class: api.NetworkHoldPublicTarget, Expires: expires}
		case api.OpPolicyResolve:
			resolves++
			var actual api.NetworkHoldActionArgs
			if json.Unmarshal(req.Args, &actual) != nil || actual.Request != args.Request || actual.Session != args.Session || actual.Scope != args.Scope || actual.Revision != 4 || actual.Maximum != api.NetworkHoldPublicOnly {
				t.Fatal("resolution changed the original action or its restrictive maximum")
			}
			return api.NetworkHoldResolution{Version: 1, Request: args.Request, Revision: 4, Scope: args.Scope, Target: "api.example:443", Class: api.NetworkHoldPublicTarget, Expires: expires}
		default:
			t.Fatal("a public resolution used an unrelated RPC", req.Op)
			return nil
		}
	})
	ctx := WithGrantRequest(t.Context(), r)
	view, err := c.ResolveNetworkRequest(ctx, args)
	if err != nil || r.networkHold.config == nil || r.networkHold.resolution == nil || describes != 1 || resolves != 1 {
		t.Fatal("actual original config/source and complete response were not retained", err)
	}
	view.Target = "127.0.0.1:443"
	again, err := c.ResolveNetworkRequest(ctx, args)
	if err != nil || again.Target != "api.example:443" || resolves != 1 {
		t.Fatal("a changed public DTO replaced or renewed the private result", err)
	}
	if _, err := c.PublishNetworkRequest(ctx, args, FileLayer{Kind: api.LayerProfile, Name: "other"}); err == nil {
		t.Fatal("a returned target or caller-selected layer retargeted the original file action")
	}
}

func TestHeldResolutionRejectsChangedOriginalSettingsBeforeRetainingTarget(t *testing.T) {
	c, r, args, _ := positiveHoldFixtureV1(t, api.NetworkHoldProfile, "")
	expires := time.Now().Add(time.Minute)
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == api.OpPolicyDescribe {
			return api.NetworkHoldDescriptor{Version: 1, Request: args.Request, Revision: 4, Class: api.NetworkHoldPublicTarget, Expires: expires}
		}
		if req.Op != api.OpPolicyResolve {
			t.Fatal("changed-source resolution used an unrelated RPC", req.Op)
		}
		if err := os.WriteFile(settingsPath(), []byte("[network]\nupload_budget='32M'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return api.NetworkHoldResolution{Version: 1, Request: args.Request, Revision: 4, Scope: args.Scope, Target: "api.example:443", Class: api.NetworkHoldPublicTarget, Expires: expires}
	})
	if result, err := c.ResolveNetworkRequest(WithGrantRequest(t.Context(), r), args); err == nil || result.Target != "" || r.networkHold.resolution != nil {
		t.Fatal("changed original settings retained a prospective public target", err)
	}
}
