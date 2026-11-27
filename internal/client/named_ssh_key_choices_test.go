package client

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestNamedSSHChoicesSelectNamesWithoutChangingCreationOrGuestDomain(t *testing.T) {
	pin := json.RawMessage(`{"v":1,"fixture":"immutable"}`)
	policy := &SandboxPolicy{PolicySources: bytes.Clone(pin), Secrets: []string{"GUEST"},
		sshKeyChoicesRaw: json.RawMessage(`{"v":1,"allow":["EXTRA"],"revoke":["REMOVED"]}`)}
	original := []string{"REMOVED", "KEPT"}
	wanted := []string{"EXTRA", "KEPT"}
	names, err := applyNamedSSHKeyChoicesV1(policy, original)
	if err != nil || !reflect.DeepEqual(names, wanted) || !reflect.DeepEqual(original, []string{"REMOVED", "KEPT"}) {
		t.Fatalf("literal overrides changed the original list or lost order: %v", err)
	}
	if !bytes.Equal(policy.PolicySources, pin) || !reflect.DeepEqual(policy.Secrets, []string{"GUEST"}) {
		t.Fatal("the next-start projection changed creation identity or guest secrets")
	}
	copy := cloneSandboxPolicy(policy)
	if !sameSandboxCreation(policy, copy) {
		t.Fatal("an owned clone lost the exact source")
	}
	copy.sshKeyChoicesRaw[5] = '2'
	if sameSandboxCreation(policy, copy) || policy.sshKeyChoicesRaw[5] != '1' {
		t.Fatal("changed local choices did not invalidate an earlier action")
	}
}

func TestNamedSSHChoicesRefuseFutureMalformedAndOverlappingActions(t *testing.T) {
	for _, raw := range []string{
		`{"v":2,"allow":["KEY"]}`, `{"v":1,"allow":["KEY"],"revoke":["KEY"]}`,
		`{"v":1,"allow":["KEY","KEY"]}`, `{"v":1,"allow":["Z","A"]}`,
		`{"v":1,"allow":["SSH_AUTH_SOCK"]}`, `{"v":1,"Allow":["KEY"]}`,
		`{"v":1,"allow":["KEY"],"value":"forbidden"}`, `{"v":1,"v":1}`,
		`{"allow":["KEY"]}`, `null`,
	} {
		policy := &SandboxPolicy{sshKeyChoicesRaw: json.RawMessage(raw)}
		if _, err := applyNamedSSHKeyChoicesV1(policy, []string{"ORIGINAL"}); err == nil {
			t.Fatalf("invalid next-start metadata created a desired selection: %s", raw)
		}
	}
}
