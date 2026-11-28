package host

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestAllAgentStateDirsAndKeepValidation(t *testing.T) {
	dirs := allAgentStateDirs()
	for _, a := range agents.Table {
		for _, want := range a.StateDirs {
			if !slices.Contains(dirs, want) {
				t.Errorf("allAgentStateDirs missing %q from agent %s", want, a.Kind)
			}
		}
	}
	got, err := validateKeepFiles([]string{".bash_history", ".bash_history"})
	if err != nil || !slices.Equal(got, []string{".bash_history"}) {
		t.Fatalf("validateKeepFiles = %v, %v", got, err)
	}
	if _, err := validateKeepFiles([]string{".bashrc"}); err == nil {
		t.Fatal("expected keep_refused error for .bashrc")
	}
}

func TestSandboxStopDeleteAndResumeArgvCommand(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	now := time.Now().UTC()
	pID, c1ID, c2ID := "sbprm222", "sbchd333", "sbchd444"
	pm := &Manifest{
		Schema: 1, ID: pID, Name: "app", CreatedAt: now,
		Agent:  AgentInfo{Kind: "claude", Argv: []string{"claude"}},
		Cwd:    "/work/app",
		Status: Status{State: StateWorking, Since: now},
		Wall:   &WallRecord{Level: "process", Driver: "process"},
	}
	c1 := &Manifest{
		Schema: 1, ID: c1ID, Name: "app-sh", SandboxOf: pID, CreatedAt: now.Add(time.Second),
		Agent:  AgentInfo{Kind: "shell", Argv: []string{"/bin/bash"}},
		Cwd:    "/work/app",
		Status: Status{State: StateWorking, Since: now},
		Wall:   &WallRecord{Level: "process", Driver: "process"},
	}
	c2 := &Manifest{
		Schema: 1, ID: c2ID, Name: "dev", SandboxOf: pID, CreatedAt: now.Add(2 * time.Second),
		Agent:  AgentInfo{Kind: "custom", Argv: []string{"npm", "run", "dev"}, Command: []string{"npm", "run", "dev"}},
		Cwd:    "/work/app",
		Status: Status{State: StateWorking, Since: now},
		Wall:   &WallRecord{Level: "process", Driver: "process"},
	}
	for _, mm := range []*Manifest{pm, c1, c2} {
		if err := mm.Save(); err != nil {
			t.Fatal(err)
		}
	}

	// Default child naming avoids collisions: app-sh is taken, so next is app-sh2.
	if name := defaultChildName("app", pID, "shell"); name != "app-sh2" {
		t.Fatalf("defaultChildName = %q, want app-sh2", name)
	}

	// Custom command survives wake via resumeArgv.
	argv, _, err := resumeArgv(c2)
	if err != nil || !slices.Equal(argv, []string{"npm", "run", "dev"}) {
		t.Fatalf("resumeArgv(c2) = %v, %v; want [npm run dev]", argv, err)
	}

	// Stop on a child stops the whole sandbox and lists all 3 sessions.
	res, err := Stop(api.StopArgs{ID: c1ID})
	if err != nil {
		t.Fatalf("Stop(c1): %v", err)
	}
	if len(res.Sessions) != 3 {
		t.Fatalf("Stop.Sessions = %d, want 3", len(res.Sessions))
	}
	for _, id := range []string{pID, c1ID, c2ID} {
		lm, lerr := LoadManifest(id)
		if lerr != nil || lm.Status.State != StateStopped {
			t.Fatalf("session %s state after stop = %v, %v", id, lm, lerr)
		}
	}

	// Delete on child removes ONLY that child, keeping primary and sibling.
	if _, err := Delete(c1ID); err != nil {
		t.Fatalf("Delete(c1): %v", err)
	}
	if _, err := os.Stat(paths.SessionDir(c1ID)); !os.IsNotExist(err) {
		t.Fatal("child c1 directory still exists")
	}
	if _, err := LoadManifest(pID); err != nil {
		t.Fatalf("primary deleted when removing child: %v", err)
	}

	// Delete on primary destroys primary AND remaining child c2.
	dres, err := Delete(pID)
	if err != nil {
		t.Fatalf("Delete(primary): %v", err)
	}
	if len(dres.Sessions) != 2 {
		t.Fatalf("Delete(primary).Sessions = %d, want 2", len(dres.Sessions))
	}
	if _, err := os.Stat(paths.SessionDir(c2ID)); !os.IsNotExist(err) {
		t.Fatal("remaining child c2 still exists after primary delete")
	}
}

func TestStartDeferredRefusesChildSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	now := time.Now().UTC()
	child := &Manifest{
		Schema: 1, ID: "sbchd555", Name: "app-sh", SandboxOf: "sbprm222", CreatedAt: now,
		Agent:  AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:    "/work/app",
		Status: Status{State: StateWorking, Since: now},
		Wall:   &WallRecord{Level: "process", Driver: "process"},
	}
	if err := child.Save(); err != nil {
		t.Fatal(err)
	}

	_, err := StartDeferred(api.SessionStartArgs{ID: child.ID})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "bad_request" || pe.Hint == "" {
		t.Fatalf("StartDeferred(child) = %v, want *proto.Error{Code: bad_request} with Hint", err)
	}
}
