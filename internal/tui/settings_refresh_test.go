package tui

import (
	"reflect"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Resume and wizard actions use an edit made outside the editor. They
// must return that snapshot to the model even when the action fails.
func TestExternalSettingsAtResumeAndWizard(t *testing.T) {
	for _, action := range []string{"resume", "wizard"} {
		t.Run(action, func(t *testing.T) {
			var hs *harness
			var run func()
			if action == "resume" {
				hs = endedLab(t, 80, 24)
				h := hs.m.hostByName("gcp-dev")
				h.host.Secrets = []string{"TOKEN"}
				for i := range hs.be.hosts {
					if hs.be.hosts[i].Name == "gcp-dev" {
						hs.be.hosts[i].Secrets = []string{"TOKEN"}
					}
				}
				for i := range h.sessions {
					if h.sessions[i].ID == "g3" {
						h.sessions[i].Secrets = []string{"TOKEN"}
					}
				}
				hs.be.secretErr = &client.UserError{Msg: "secret unavailable", Hint: "Try again"}
				run = func() { hs.key("u") }
			} else {
				be := newFakeBackend()
				be.probe = client.Platform{OS: "linux", Arch: "arm64"}
				be.installed = true
				be.state["pi"] = &fakeHost{checks: []client.AgentCheck{agentReady("claude", "Claude Code", api.LoginNo)}}
				hs = newHarness(t, be, 80, 24)
				hs.key("a")
				hs.typeText("pi")
				hs.key("enter", "enter", "1", "enter", "enter")
				w := hs.m.modal.(*wizard)
				if w.step != wzRun || w.act.Kind != "login" {
					t.Fatalf("step %v, act %+v, err %v", w.step, w.act, w.err)
				}
				be.state["pi"].newErr = &client.UserError{Msg: "start unavailable", Hint: "Try again"}
				run = func() { hs.key("enter") }
			}
			fresh := keptSettingsFor(hs.be)
			setFile(hs.be, fresh, "")
			run()
			usedKept(t, hs.be, action == "wizard")
			if !reflect.DeepEqual(hs.m.settings, fresh) {
				t.Fatalf("the model kept %+v, want the external edit %+v", hs.m.settings, fresh)
			}
			setFile(hs.be, client.DefaultSettings(), brokenWarning)
			run()
			usedKept(t, hs.be, action == "wizard")
			if !reflect.DeepEqual(hs.m.settings, fresh) {
				t.Fatal("a broken external edit replaced the kept settings")
			}
			want := brokenWarning + ". The launcher keeps the settings it had."
			if !slices.Contains(toastTexts(hs.m), want) {
				t.Fatalf("no retained-settings warning: %q", toastTexts(hs.m))
			}
		})
	}
}

// An attach reads after it receives the terminal, rather than taking a
// stale snapshot while an earlier command still owns the terminal.
func TestAttachRefreshesSettingsWhenItRuns(t *testing.T) {
	hs := sessions(t, 80, 24)
	h := hs.m.hostByName(hs.m.cur)
	if h == nil || len(h.sessions) == 0 {
		t.Fatal("no session to attach")
	}
	s := h.sessions[0]
	fresh := keptSettingsFor(hs.be)
	fresh.Attach.DetachKey = "ctrl+]"
	fresh.Attach.ImagePasteKey = "ctrl+x"
	fresh.Attach.Scrollback = "clear"
	attach := hs.m.attachTo(h, s.ID, s.Name)
	setFile(hs.be, fresh, "")
	hs.do(attach)
	check := func() {
		t.Helper()
		hs.be.mu.Lock()
		got := hs.be.attached[len(hs.be.attached)-1].Settings
		hs.be.mu.Unlock()
		if !reflect.DeepEqual(got, fresh) || !reflect.DeepEqual(hs.m.settings, fresh) {
			t.Fatalf("attach settings %+v, model %+v, want %+v", got, hs.m.settings, fresh)
		}
	}
	check()
	setFile(hs.be, client.DefaultSettings(), brokenWarning)
	hs.do(hs.m.attachTo(h, s.ID, s.Name))
	check()
	want := brokenWarning + ". The launcher keeps the settings it had."
	if !slices.Contains(toastTexts(hs.m), want) {
		t.Fatalf("no broken-attach warning: %q", toastTexts(hs.m))
	}
}

// Applying settings also refreshes animation, including the environment's
// auto rule. A rejected edit preserves both the snapshot and animation.
func TestSettingsRefreshesMotion(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("INSIDE_EMACS", "")
	t.Setenv("TERMINAL_EMULATOR", "")
	hs := home(t, 80, 24)
	apply := func(s client.Settings, warning string) {
		read := hs.m.beginSettingsRead()
		hs.send(settingsMsg{s: s, warning: warning, generation: read.generation})
	}
	for _, tt := range []struct {
		setting string
		motion  bool
	}{{"on", true}, {"off", false}, {"auto", true}} {
		s := hs.be.Settings()
		s.UI.Motion = tt.setting
		apply(s, "")
		if hs.m.motion != tt.motion {
			t.Fatalf("motion %q = %v, want %v", tt.setting, hs.m.motion, tt.motion)
		}
	}
	t.Setenv("NO_COLOR", "1")
	apply(hs.m.settings, "")
	if hs.m.motion {
		t.Fatal("auto motion ignored NO_COLOR")
	}
	s := hs.m.settings
	s.UI.Motion = "on"
	apply(s, brokenWarning)
	if hs.m.motion || hs.m.settings.UI.Motion != "auto" {
		t.Fatal("a rejected edit changed motion or settings")
	}
	// Exercise Update's generic action-message path as well as the editor.
	read := hs.m.beginSettingsRead()
	read.fresh = &s
	hs.send(struct{ settingsRead }{read})
	if !hs.m.motion {
		t.Fatal("a successful action read did not update motion")
	}
}
