package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNamedSSHPolicyAcknowledgementCannotChangeTheLocalAction(t *testing.T) {
	plan := &namedSSHKeyPolicyPlanV1{args: api.NamedSSHKeyPolicyArgs{V: 1, Session: "abcd2345", Name: "DEPLOY_KEY", Allow: true}}
	for _, raw := range []string{
		`{"v":1,"names":["OTHER_KEY"],"applies":"next_start"}`,
		`{"v":1,"names":["DEPLOY_KEY"],"applies":"at_once"}`,
		`{"v":1,"names":["DEPLOY_KEY","DEPLOY_KEY"],"applies":"next_start"}`,
		`{"v":1,"names":["OTHER_KEY","DEPLOY_KEY"],"applies":"next_start"}`,
		`{"v":1,"names":["DEPLOY_KEY"],"applies":"next_start","extra":true}`,
		`{"v":1,"names":["DEPLOY_KEY"],"applies":"next_start","V":1}`,
		`{"v":1,"names":["DEPLOY_KEY"],"applies":"next_start","v":1}`,
		`{"v":2,"names":["DEPLOY_KEY"],"applies":"next_start"}`,
	} {
		if _, err := plan.acknowledged([]byte(raw)); err == nil {
			t.Fatal("invalid or mismatched ACK approved a local choice")
		}
	}
	result, err := plan.acknowledged([]byte(`{"v":1,"names":["DEPLOY_KEY","REMOTE_ONLY"],"applies":"next_start"}`))
	if err != nil || len(result.Names) != 2 || plan.args.Name != "DEPLOY_KEY" || !plan.args.Allow {
		t.Fatal("a host list changed the literal local action", err)
	}
	// Recording receives only plan.args.Name/Allow, never result.Names.
	plan.args.Allow = false
	if _, err := plan.acknowledged([]byte(`{"v":1,"names":["DEPLOY_KEY"],"applies":"next_start"}`)); err == nil {
		t.Fatal("a failed removal was recorded")
	}
	if _, err := plan.acknowledged([]byte(`{"v":1,"names":[],"applies":"next_start"}`)); err != nil {
		t.Fatal("valid next-start removal refused", err)
	}
}
