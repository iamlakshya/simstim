package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Direct owner oracles retain startedMsg without delivering it to Update.
// Drive the new readonly pre-request phase through its actual model handler.
// Reconnect ownership is tested with the full model scheduler separately.
func startResult(t *testing.T, model *Model, command tea.Cmd) (startedMsg, bool) {
	t.Helper()
	for command != nil {
		message := command()
		switch result := message.(type) {
		case startedMsg:
			return result, true
		case newStartSourceMsg:
			command = model.onNewStartSource(result)
		default:
			t.Fatalf("unexpected start owner message %T", message)
		}
	}
	return startedMsg{}, false
}
