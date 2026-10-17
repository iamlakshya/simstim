package agents

import "testing"

func TestEveryAgentHasASupportLevel(t *testing.T) {
	seen := map[string]bool{}
	for i, a := range Table {
		switch a.Support {
		case FirstClass, Supported, BestEffort:
		default:
			t.Errorf("%s: support level %q", a.Kind, a.Support)
		}
		if seen[a.Kind] {
			t.Errorf("%s: twice in the table", a.Kind)
		}
		seen[a.Kind] = true
		if len(a.Launch) == 0 || len(a.Binaries) == 0 {
			t.Errorf("%s: no launch command or binary", a.Kind)
		}
		if i > 0 && rank(a.Support) < rank(Table[i-1].Support) && a.Kind != "shell" {
			t.Errorf("%s (%s) comes after %s (%s): list the more supported agents first", a.Kind, a.Support, Table[i-1].Kind, Table[i-1].Support)
		}
	}
}

func TestFirstClassAgents(t *testing.T) {
	// Claude Code and Codex are first-class from day one: hooks for status,
	// and resume after a reboot.
	for _, k := range []string{"claude", "codex"} {
		a, ok := Find(k)
		if !ok || a.Support != FirstClass {
			t.Fatalf("%s: want first-class, got %+v", k, a.Support)
		}
		if len(a.Hooks) == 0 || len(a.Resume) == 0 || len(a.Continue) == 0 {
			t.Errorf("%s: a first-class agent needs hooks, resume and continue", k)
		}
	}
}

func TestAntigravity(t *testing.T) {
	a, ok := Find("antigravity")
	if !ok || a.Binaries[0] != "agy" {
		t.Fatalf("antigravity: %+v", a)
	}
	got := Expand(a.Resume, map[string]string{"agent_session_id": "c-123"})
	if want := []string{"agy", "--conversation", "c-123"}; len(got) != 3 || got[2] != want[2] {
		t.Errorf("resume = %v, want %v", got, want)
	}
}

// The memory that each agent's own docs ask for, where they name it.
func TestMemory(t *testing.T) {
	want := map[string]int64{"claude": 4 * GB, "codex": 4 * GB}
	for _, a := range Table {
		if a.Memory != want[a.Kind] {
			t.Errorf("%s asks for %d bytes, want %d", a.Kind, a.Memory, want[a.Kind])
		}
	}
}

func rank(s Support) int {
	return map[Support]int{FirstClass: 0, Supported: 1, BestEffort: 2}[s]
}

func TestEveryCheckedAgentCanBeInstalled(t *testing.T) {
	for _, a := range Table {
		if (a.Support == FirstClass || a.Support == Supported) && a.Kind != "shell" {
			if a.Setup == nil || a.Setup.Install == "" || a.Setup.Source == "" || a.Setup.Login == "" {
				t.Errorf("%s: a first-class or supported agent needs an install command, its source and a login", a.Kind)
			}
		}
	}
}
