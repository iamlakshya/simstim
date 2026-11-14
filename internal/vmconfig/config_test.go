package vmconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func validVMPrepare() proto.CtlVMPrepareArgs {
	return proto.CtlVMPrepareArgs{Session: "abcdefgh", VM: proto.VMStartV1{Version: 1, System: "reset", StateDirs: []string{".claude/projects"}}, StateFiles: []string{".claude.json"}, CaptureNonce: strings.Repeat("a", 32)}
}

func TestVMPrepareRejectsUnsafeStateAndReusedNonceBeforeEffects(t *testing.T) {
	for _, mutate := range []func(*proto.CtlVMPrepareArgs){
		func(a *proto.CtlVMPrepareArgs) { a.Session = "../escape" },
		func(a *proto.CtlVMPrepareArgs) { a.VM.Version = 2 },
		func(a *proto.CtlVMPrepareArgs) { a.VM.System = "arbitrary" },
		func(a *proto.CtlVMPrepareArgs) { a.VM.StateDirs = []string{"/etc"} },
		func(a *proto.CtlVMPrepareArgs) { a.StateFiles = []string{"../../root"} },
		func(a *proto.CtlVMPrepareArgs) { a.VM.RestoreCaptureNonce = a.CaptureNonce },
		func(a *proto.CtlVMPrepareArgs) { a.CaptureNonce = "" },
		func(a *proto.CtlVMPrepareArgs) { a.VM.RestoreCaptureNonce = strings.Repeat("A", 32) },
	} {
		a := validVMPrepare()
		mutate(&a)
		if _, err := Validate(a); err == nil {
			t.Fatal("invalid protected VM setup accepted", a)
		}
	}
}

func TestVMPrepareCodecRejectsAliasesDuplicatesAndTrailingData(t *testing.T) {
	valid := `{"session":"abcdefgh","vm":{"version":1,"system":"reset","sudo":false,"docker":false,"state_dirs":[]},"state_files":[],"capture_nonce":""}`
	if _, err := Decode([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"session":`, `"Session":`, 1),
		strings.Replace(valid, `"sudo":false`, `"sudo":false,"sudo":true`, 1),
		strings.Replace(valid, `"docker":false`, `"Docker":true`, 1),
		strings.Replace(valid, `"system":"reset"`, `"system":"reset","uid":0`, 1),
		valid + ` {}`, `null`, strings.Replace(valid, `"version":1`, `"version":"1"`, 1),
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("ambiguous or open VM prepare accepted", bad)
		}
	}
}

func TestVMPrepareClonesProtectedSlicesAndKeepsResolvedOptions(t *testing.T) {
	a := validVMPrepare()
	a.VM.Sudo = true
	a.VM.Docker = true
	a.VM.System = "keep"
	plan, err := Validate(a)
	if err != nil {
		t.Fatal(err)
	}
	a.VM.StateDirs[0] = "changed"
	a.StateFiles[0] = "changed"
	if plan.State.Dirs[0] != ".claude/projects" || plan.Args.StateFiles[0] != ".claude.json" {
		t.Fatal("caller mutated validated state")
	}
	encoded, err := json.Marshal(plan.Args)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !roundtrip.Args.VM.Sudo || !roundtrip.Args.VM.Docker || roundtrip.Args.VM.System != "keep" {
		t.Fatal("resolved VM flags were reset")
	}
}

func TestVMMemberCodecUsesExistingExactDTOWithoutAliasesOrOpenFields(t *testing.T) {
	data := `{"session":"abcdefgh","run_token":"one-run","argv":["/bin/sh"],"env":["HOME=/home/agent"],"cwd":"/work","cols":80,"rows":24}`
	if _, err := DecodeMember([]byte(data)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(data, `"session":`, `"Session":`, 1), strings.Replace(data, `"cols":80`, `"cols":80,"cols":120`, 1), strings.Replace(data, `"rows":24`, `"rows":24,"uid":0`, 1), data + ` {}`, `null`} {
		if _, err := DecodeMember([]byte(bad)); err == nil {
			t.Fatal("ambiguous VM member data accepted", bad)
		}
	}
}
