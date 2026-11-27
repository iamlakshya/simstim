package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// Confirm (docs/contributing/ux-spec.md 2.5).

type confirm struct {
	title  string
	lines  []string
	yes    hint
	no     hint
	danger bool
	run    func(m *Model) tea.Cmd
}

func (c *confirm) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter", "y":
		m.modal = nil
		return c.run(m)
	case "esc", "n", "q":
		m.modal = nil
	}
	return nil
}

func (c *confirm) view(m *Model) string {
	w := m.dialogWidth()
	body := make([]string, 1, len(c.lines)+2) // a blank line first
	for _, l := range c.lines {
		body = append(body, wrap(l, w-4)...)
	}
	body = append(body, "")
	t := toneNormal
	if c.danger {
		t = toneDanger
	}
	return sheet(w, c.title, "", []hint{c.yes, c.no}, trimBlank(body), t)
}

// deleteDialog asks before a session is deleted, with the lines that the
// rm command asks with too (client.DeleteLines).
func deleteDialog(h *hostConn, s client.Session) *confirm {
	host := h.name()
	return &confirm{
		title: "Delete " + shown(s.Name) + "?", lines: client.DeleteLines(s, host, homePath(h, s.Cwd)), danger: true,
		yes: hint{"enter", "delete"}, no: hint{"esc", "keep"},
		run: func(m *Model) tea.Cmd { return m.deleteSession(host, s) },
	}
}

func removeHostDialog(h client.Host) *confirm {
	name := shown(h.Name)
	return &confirm{
		title: "Remove " + name + "?",
		lines: []string{
			fmt.Sprintf("This removes %s from the hosts on this computer.", name),
			"Its sessions and the helper stay on the host, and keep running.",
		},
		danger: true, yes: hint{"enter", "remove"}, no: hint{"esc", "keep"},
		// The entry that the dialog showed: a host made again while it
		// waited is not removed.
		run: func(m *Model) tea.Cmd { return m.changeHost(client.HostChange{Was: &h}) },
	}
}

// Host details (docs/contributing/ux-spec.md 2.9).

type details struct{ host string }

func (d *details) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	h := m.hostByName(d.host)
	if h == nil {
		m.modal = nil
		return nil
	}
	switch k.String() {
	case "esc", "q":
		m.modal = nil
	case "t":
		return m.testHost(h)
	case "r":
		return m.reconnect(h)
	case "x":
		m.modal = removeHostDialog(h.host)
	case "s":
		return m.openShell(h)
	}
	return nil
}

func (d *details) view(m *Model) string {
	w := m.dialogWidth()
	cw := w - 4
	h := m.hostByName(d.host)
	if h == nil {
		return ""
	}
	field := func(k, v string) string { return sFaint.Render(fit(k, labelW)) + trunc(v, cw-labelW) }
	body := []string{"", field("Floor", hostWallFloor(h)), field("Level now", hostWallNow(h))}
	// The lines come from the client, as `host show` prints them
	// (cli-spec 11.2).
	for _, line := range (client.HostDetails{Host: h.host, Tier: h.tier, IDChanged: h.idChanged, Info: h.info, Sessions: len(h.sessions)}).Lines() {
		if len(line.Items) == 0 {
			v := line.Value
			if line.Note != "" {
				v += sFaint.Render("  " + line.Note)
			}
			body = append(body, field(line.Label, v))
			continue
		}
		// A list goes on more lines rather than lose its items, such as
		// the agents' versions.
		for n, l := range joinWrap(line.Items, cw-labelW) {
			k := ""
			if n == 0 {
				k = line.Label
			}
			body = append(body, field(k, l))
		}
	}
	if h.info == nil {
		msg, _ := errText(h.err)
		msg = oneLine(msg)
		if msg == "" {
			msg = "waiting for the host to answer"
		}
		body = append(body, field("Status", sFaint.Render(sentence(shown(msg)))))
	}
	body = append(body, "")
	hints := []hint{{"t", "test"}, {"s", "shell"}, {"r", "reconnect"}, {"x", "remove"}, {"esc", "back"}}
	title := shown(h.name())
	return sheet(w, title, m.hostState(h, w-8-cells(title)), hints, trimBlank(body), toneNormal)
}
