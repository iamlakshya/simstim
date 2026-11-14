package tui

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// snapshotSettingsBackend holds one immutable read while the UI moves on.
// All action methods still use the normal fake backend and its locks.
type snapshotSettingsBackend struct {
	Backend
	s                client.Settings
	warning          string
	entered, release chan struct{}
}

func (b snapshotSettingsBackend) ReadSettings() (client.Settings, string) {
	if b.entered != nil {
		close(b.entered)
		<-b.release
	}
	return b.s, b.warning
}

// Every action stamps its read before its command runs. A newer editor
// reload must survive even when an older queued action completes later.
func TestQueuedSettingsReadsCannotReplaceAnEditorReload(t *testing.T) {
	for _, action := range []string{"start", "resume", "wizard", "attach"} {
		for _, warning := range []string{"", brokenWarning} {
			t.Run(action+"/"+warning, func(t *testing.T) {
				var hs *harness
				var run func() tea.Cmd
				if action == "wizard" {
					be := newFakeBackend()
					be.probe = client.Platform{OS: "linux", Arch: "arm64"}
					be.installed = true
					be.state["pi"] = &fakeHost{checks: []client.AgentCheck{agentReady("claude", "Claude Code", api.LoginNo)}}
					hs = newHarness(t, be, 80, 24)
					hs.key("a")
					hs.typeText("pi")
					hs.key("enter", "enter", "enter", "enter")
					w := hs.m.modal.(*wizard)
					be.state["pi"].newErr = errors.New("start unavailable")
					run = func() tea.Cmd { return w.runAct(hs.m) }
				} else if action == "resume" {
					hs = endedLab(t, 80, 24)
					h := hs.m.hostByName("gcp-dev")
					h.host.Secrets = []string{"TOKEN"}
					for i := range h.sessions {
						if h.sessions[i].ID == "g3" {
							h.sessions[i].Secrets = []string{"TOKEN"}
						}
					}
					hs.be.secretErr = errors.New("secret unavailable")
					run = hs.m.resumeSelected
				} else {
					hs = sessions(t, 80, 24)
					h, s := hs.m.selectedPair()
					if h == nil || s == nil {
						t.Fatal("no selected session")
					}
					if action == "attach" {
						hs.be.attachErr = errors.New("attach unavailable")
						run = func() tea.Cmd { return hs.m.attachTo(h, s.ID, s.Name) }
					} else {
						hs.be.secretErr = errors.New("secret unavailable")
						run = func() tea.Cmd {
							return hs.m.startSession(h, startReq{args: client.NewArgs{Agent: "shell", Cwd: "/srv/app"}})
						}
					}
				}
				older := client.DefaultSettings()
				older.UI.Motion = "off"
				older.Sessions.MemoryMax = "1G"
				hs.m.be = snapshotSettingsBackend{Backend: hs.be, s: older, warning: warning}
				queued := run()
				// The newer read is delivered before the old command even starts.
				hs.m.be = hs.be
				newer := keptSettingsFor(hs.be)
				newer.Attach.DetachKey = "ctrl+]"
				newer.UI.Motion = "on"
				setFile(hs.be, newer, "")
				hs.send(editorDoneMsg{})
				hs.do(queued)
				if !reflect.DeepEqual(hs.m.settings, newer) || !hs.m.motion || hs.m.settingsWarned != "" {
					t.Fatalf("old %s read replaced the editor snapshot: %+v, motion %v, warning %q", action, hs.m.settings, hs.m.motion, hs.m.settingsWarned)
				}
				if slices.Contains(toastTexts(hs.m), brokenWarning+". The launcher keeps the settings it had.") {
					t.Fatal("an obsolete action warning was shown")
				}
			})
		}
	}
}

// An already-running reader cannot race with or undo a UI settings update.
// Channels hold the read without sleeps, so the completion order is fixed.
func TestInFlightSettingsReadKeepsTheNewerSnapshot(t *testing.T) {
	hs := home(t, 80, 24)
	for _, warning := range []string{"", brokenWarning} {
		older := client.DefaultSettings()
		older.UI.Motion = "off"
		be := snapshotSettingsBackend{Backend: hs.be, s: older, warning: warning, entered: make(chan struct{}), release: make(chan struct{})}
		read, kept := hs.m.beginSettingsRead(), hs.m.settings
		result := make(chan settingsRead, 1)
		go func() {
			_, completed := readSettings(be, kept, read)
			result <- completed
		}()
		select {
		case <-be.entered:
		case <-time.After(20 * time.Second):
			close(be.release)
			t.Fatal("the settings worker never reached its read barrier")
		}
		newer := client.DefaultSettings()
		newer.Attach.DetachKey = "ctrl+]"
		newer.UI.Motion = "on"
		hs.m.useSettings(newer)
		close(be.release)
		if cmd := hs.m.takeSettings(<-result); cmd != nil || !reflect.DeepEqual(hs.m.settings, newer) || hs.m.settingsWarned != "" {
			t.Fatal("an old in-flight read changed the accepted UI snapshot or warning")
		}
	}
}

// Closing an editor invalidates old results before its own reload has
// completed, so an old action cannot briefly restore an obsolete snapshot.
func TestEditorReloadImmediatelyInvalidatesEarlierReads(t *testing.T) {
	hs := home(t, 80, 24)
	kept := hs.m.settings
	older := client.DefaultSettings()
	older.Attach.DetachKey = "ctrl+x"
	read := hs.m.beginSettingsRead()
	read.fresh = &older
	newer := keptSettingsFor(hs.be)
	newer.Attach.DetachKey = "ctrl+]"
	setFile(hs.be, newer, "")
	reload := hs.m.onEditorDone(editorDoneMsg{})
	hs.m.takeSettings(read)
	if !reflect.DeepEqual(hs.m.settings, kept) {
		t.Fatal("an old action changed settings while the editor reload was pending")
	}
	hs.do(reload)
	if !reflect.DeepEqual(hs.m.settings, newer) {
		t.Fatal("the current editor reload was not applied")
	}
}

// Editor reloads have the same ordering rule as action reads.
func TestOldEditorReloadCannotReplaceANewerReload(t *testing.T) {
	for _, warning := range []string{"", brokenWarning} {
		hs := home(t, 80, 24)
		older := client.DefaultSettings()
		older.UI.Motion = "off"
		hs.m.be = snapshotSettingsBackend{Backend: hs.be, s: older, warning: warning}
		queued := hs.m.onEditorDone(editorDoneMsg{})
		hs.m.be = hs.be
		newer := keptSettingsFor(hs.be)
		newer.Attach.DetachKey = "ctrl+]"
		setFile(hs.be, newer, "")
		hs.send(editorDoneMsg{})
		hs.do(queued)
		if !reflect.DeepEqual(hs.m.settings, newer) || hs.m.settingsWarned != "" {
			t.Fatalf("an old editor result replaced newer settings: %+v, %q", hs.m.settings, hs.m.settingsWarned)
		}
	}
}
