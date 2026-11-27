//go:build linux

package guest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestVMAgentSecretsEnterOnlyInitOwnedChildEnvironment(t *testing.T) {
	args := proto.CtlStartArgs{Env: []string{"PATH=/usr/bin:/bin", "HOME=/home/agent"}, Secrets: map[string]string{"Z_SECRET": "last-private", "A_SECRET": "first-private"}}
	original := append([]string(nil), args.Env...)
	env := vmAgentEnvironment(args, true)
	if !reflect.DeepEqual(args.Env, original) {
		t.Fatal("holder-visible environment was mutated with secrets")
	}
	want := append(original, "DOCKER_HOST=unix:///run/"+brand.Name+"/docker.sock", "A_SECRET=first-private", "Z_SECRET=last-private")
	if !reflect.DeepEqual(env, want) {
		t.Fatal("owned child environment lost explicit values or deterministic ordering")
	}
	visible := strings.Join(holderEnv(original, "/home/agent"), "\n")
	if strings.Contains(visible, "private") {
		t.Fatal("secret values entered the UID900 holder")
	}
}

func TestVMOwnedPTYStartRejectsMissingCapabilityBeforeImageExec(t *testing.T) {
	v := &vmRuntime{ptys: map[string]*vmPTY{}}
	if _, err := v.startAgent(proto.CtlStartArgs{Session: "abcdefgh", RunToken: "missing", Argv: []string{"/bin/sh"}}, nil); err == nil {
		t.Fatal("PTY child could start without inherited member capability")
	}
}

func TestVMHolderCredentialIsFixed900WithNoAgentGroups(t *testing.T) {
	vm := &vmRuntime{applied: true}
	attr, err := vm.holderProcess()
	if err != nil {
		t.Fatal(err)
	}
	if attr.Credential == nil || attr.Credential.Uid != 900 || attr.Credential.Gid != 900 || len(attr.Credential.Groups) != 0 || attr.Credential.NoSetGroups {
		t.Fatal("holder inherited root/agent/foreign identity", attr)
	}
}

func TestVMPrimaryAndSecondaryMembersCannotReplacePreparedState(t *testing.T) {
	nonce := strings.Repeat("a", 32)
	vm := &vmRuntime{applied: true, network: &vmNetwork{dns: &DNS{}, proxy: &Proxy{}}, plan: vmPreparePlan{Args: proto.CtlVMPrepareArgs{Session: "abcdefgh", StateFiles: []string{".claude.json"}, CaptureNonce: nonce}}}
	primary := proto.CtlStartArgs{Session: "abcdefgh", RunToken: "primary-run", DNS: proto.DNSSynthetic, StateFiles: []string{".claude.json"}, CaptureNonce: nonce}
	if err := vm.validateMember(primary, true); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*proto.CtlStartArgs){func(a *proto.CtlStartArgs) { a.Session = "different" }, func(a *proto.CtlStartArgs) { a.CaptureNonce = "" }, func(a *proto.CtlStartArgs) { a.StateFiles = nil }, func(a *proto.CtlStartArgs) { a.RunToken = "" }} {
		args := primary
		mutate(&args)
		if vm.validateMember(args, true) == nil {
			t.Fatal("primary replaced prepared identity/state/capability")
		}
	}
	child := proto.CtlStartArgs{Session: "child", RunToken: "child-run"}
	if err := vm.validateMember(child, false); err != nil {
		t.Fatal(err)
	}
	child.StateFiles = []string{".claude.json"}
	if vm.validateMember(child, false) == nil {
		t.Fatal("secondary replaced sandbox state")
	}
	child.StateFiles = nil
	child.CaptureNonce = nonce
	if vm.validateMember(child, false) == nil {
		t.Fatal("secondary claimed controller's state publication nonce")
	}
}
