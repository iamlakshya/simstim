package tui

import (
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// checkedSettingsBackend is implemented by backends that report config.toml
// warnings alongside loaded settings (docs/contributing/cli-spec.md 13.4).
type checkedSettingsBackend interface {
	LoadSettings() (client.Settings, string)
}

func readBackendSettings(be Backend) (client.Settings, string) {
	if cb, ok := be.(checkedSettingsBackend); ok {
		return cb.LoadSettings()
	}
	return be.Settings(), ""
}

// nextSettingsSeq allocates the next monotonic sequence number for an async
// settings reload so an older reload can never overwrite newer settings.
func (m *Model) nextSettingsSeq() uint64 {
	m.settingsSeq++
	return m.settingsSeq
}

// applySettings applies a settings read with sequence number seq. When warn is
// non-empty, the launcher keeps its existing good settings and shows the
// warning once until a good read clears it (cli-spec 13.4).
func (m *Model) applySettings(s client.Settings, warn string, seq uint64) tea.Cmd {
	if seq < m.settingsSeq {
		return nil
	}
	m.settingsSeq = seq
	if warn != "" {
		if warn == m.configWarn {
			return nil
		}
		m.configWarn = warn
		return m.toastNext(toastWarning, sentence(warn), brand.Name+" config check")
	}
	oldMotion := m.settings.UI.Motion
	m.settings = s
	m.configWarn = ""
	commands = withKeymap(registry(), m.settings.Keymap)
	if oldMotion != "" && s.UI.Motion != oldMotion {
		m.motion = motionOn(s.UI.Motion, os.Getenv)
	}
	return nil
}

// refreshSettings re-reads config.toml synchronously before an action that
// uses settings (attach, start, resume), applying any good update or warning
// once while keeping the previous good settings on error.
func (m *Model) refreshSettings() (client.Settings, tea.Cmd) {
	seq := m.nextSettingsSeq()
	s, warn := readBackendSettings(m.be)
	cmd := m.applySettings(s, warn, seq)
	return m.settings, cmd
}
