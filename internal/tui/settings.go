package tui

import (
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The settings that the launcher holds (docs/contributing/cli-spec.md
// 13.4). The launcher reads config.toml once for each attach, start, resume,
// install or login. A good read gives the
// new settings, also after an edit in another window, and the launcher
// keeps them. A config.toml that cannot be used would drop every setting:
// then the action uses the settings that the launcher holds, and the
// launcher says why, once.

// settingsRead is what one read of config.toml gave an action: the new
// settings, or the warning that keeps the old ones.
type settingsRead struct {
	fresh      *client.Settings
	warning    string
	generation uint64
}

// settingsMsg is the settings after an edit, or the warning of a file
// that cannot be used. generation was captured before the reload ran.
type settingsMsg struct {
	s          client.Settings
	warning    string
	generation uint64
}

// beginSettingsRead runs on the UI thread before an asynchronous read is
// dispatched. A later request supersedes earlier in-flight completions.
func (m *Model) beginSettingsRead() settingsRead {
	m.settingsGeneration++
	return settingsRead{generation: m.settingsGeneration}
}

// readOf lets Update find the read in the message of an action.
func (r settingsRead) readOf() settingsRead { return r }

// readSettings reads config.toml for an action, in its command. It returns
// the settings to use: the new ones, or kept, the launcher's own, when the
// file cannot be used.
func readSettings(be Backend, kept client.Settings, read settingsRead) (client.Settings, settingsRead) {
	read.fresh, read.warning = nil, ""
	s, w := be.ReadSettings()
	if w != "" {
		read.warning = w
		return kept, read
	}
	read.fresh = &s
	return s, read
}

// settingsOf takes the settings that an action read, when msg is the end
// of one.
func (m *Model) settingsOf(msg tea.Msg) tea.Cmd {
	if m.ctx.Err() != nil {
		return nil
	}
	if started, ok := msg.(actStartedMsg); ok {
		w, active := m.modal.(*wizard)
		if !active || !w.owns(started.owner, started.seq) {
			return nil
		}
	}
	if r, ok := msg.(interface{ readOf() settingsRead }); ok {
		return m.takeSettings(r.readOf())
	}
	return nil
}

// takeSettings keeps the settings that an action read, or shows its
// warning, unless the launcher showed that warning last. A good read
// clears the warning that it showed last. An obsolete read does nothing.
func (m *Model) takeSettings(r settingsRead) tea.Cmd {
	if r.generation != m.settingsGeneration {
		return nil
	}
	switch {
	case r.fresh != nil:
		m.useSettings(*r.fresh)
	case r.warning != "" && r.warning != m.settingsWarned:
		m.settingsWarned = r.warning
		return m.keptSettings(r.warning)
	}
	return nil
}

// useSettings accepts s, applies its keys and motion, and invalidates older reads.
func (m *Model) useSettings(s client.Settings) {
	m.settingsGeneration++
	m.settings, m.settingsWarned = s, ""
	commands = withKeymap(registry(), s.Keymap)
	m.motion = motionOn(s.UI.Motion, os.Getenv)
}

// keptSettings says that config.toml cannot be used, and that the launcher
// keeps the settings it had.
func (m *Model) keptSettings(warning string) tea.Cmd {
	return m.toastNext(toastWarning, warning+". The launcher keeps the settings it had.", brand.Name+" config check")
}

// onEditorDone reloads the settings after the editor closes. A config.toml
// that cannot be used gives its warning instead.
func (m *Model) onEditorDone(msg editorDoneMsg) tea.Cmd {
	be := m.be
	read := m.beginSettingsRead()
	reload := func() tea.Msg {
		s, w := be.ReadSettings()
		return settingsMsg{s: s, warning: w, generation: read.generation}
	}
	if msg.err != nil {
		return tea.Batch(reload, m.toastErr(msg.err))
	}
	return reload
}

// onSettings takes the settings after an edit in the launcher's editor. A
// config.toml that cannot be used keeps the ones the launcher has, and the
// launcher says why, each time: the user just made the edit.
func (m *Model) onSettings(msg settingsMsg) tea.Cmd {
	if msg.generation != m.settingsGeneration {
		return nil
	}
	if msg.warning != "" {
		m.settingsWarned = msg.warning
		return m.keptSettings(msg.warning)
	}
	m.useSettings(msg.s)
	return nil
}
