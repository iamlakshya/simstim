package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func isolatedStoreHostConstruction(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "se-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS", "GNUPGHOME", "TMPDIR"} {
		path := filepath.Join(root, key)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return root
}

// Unlike the guest's explicitly labelled direct-array witnesses, this oracle
// calls actual primary/member constructors. It creates protected LOCAL test
// records only. No provider, VM, container, holder or command starts here.
func TestActualWalledPrimaryAndMemberStoreMetadataDifference(t *testing.T) {
	root := isolatedStoreHostConstruction(t)
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	const primaryID, childID = "parent234", "child234"
	primaryArgs := NewArgs{ColorTerm: "truecolor", TermProgram: "primary-terminal", TermProgramVersion: "1"}
	// Empty TERM uses the actual canonical fallback without invoking infocmp.
	primaryEnv, term := walledEnv(primaryArgs, primaryID, nil, agents.Agent{}, false)
	parent := &Manifest{Schema: 1, ID: primaryID, Name: "primary", CreatedAt: time.Now().UTC(), Cwd: work, Env: primaryEnv, Term: TermInfo{Term: term}, Wall: &WallRecord{Level: "process", Driver: "plain"}}
	if err := parent.Save(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SandboxSnapshot(primaryID)
	if err != nil {
		t.Fatal(err)
	}
	member, err := PrepareSandboxMember(parent, api.JoinArgs{Parent: primaryID, ID: childID, Name: "child", Agent: "store-env-fixture", Argv: []string{"/usr/bin/true"}, ColorTerm: "24bit", TermProgram: "member-terminal", TermProgramVersion: "2", Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	defer member.Finish(false)
	childEnv := member.Manifest.Env
	if envValue(primaryEnv, brand.Env("SESSION")) != primaryID || envValue(childEnv, brand.Env("SESSION")) != childID {
		t.Fatal("actual constructors did not carry distinct session metadata")
	}
	if envValue(primaryEnv, brand.Env("HOOK_SOCK")) != GuestHookSocket() || envValue(childEnv, brand.Env("HOOK_SOCK")) != GuestRunDir()+"/hook-"+childID+".sock" {
		t.Fatal("actual member hook route did not differ from primary")
	}
	if envValue(primaryEnv, "COLORTERM") != "truecolor" || envValue(childEnv, "COLORTERM") != "24bit" || envValue(primaryEnv, "TERM_PROGRAM") != "primary-terminal" || envValue(childEnv, "TERM_PROGRAM") != "member-terminal" || envValue(primaryEnv, "TERM_PROGRAM_VERSION") != "1" || envValue(childEnv, "TERM_PROGRAM_VERSION") != "2" {
		t.Fatal("actual terminal metadata did not reflect each caller")
	}
	if envValue(primaryEnv, "TERM") != "xterm-256color" || envValue(childEnv, "TERM") != "xterm-256color" {
		t.Fatal("empty TERM fallback changed")
	}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "LANG", "PATH"} {
		if envValue(primaryEnv, key) == "" || envValue(primaryEnv, key) != envValue(childEnv, key) {
			t.Fatal("ordinary member changed a meaningful fixed base field")
		}
	}
	if member.Manifest.Cwd != parent.Cwd {
		t.Fatal("ordinary member did not retain shared Cwd")
	}
}
