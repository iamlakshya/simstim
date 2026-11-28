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

func deleteDialog(h *hostConn, s client.Session) *confirm {
	host, d := h.name(), shown(s)
	name := d.Name
	if name == "" {
		name = d.ID
	}
	return &confirm{
		title: "Delete " + name + "?", lines: client.DeleteLines(s, host, homePath(h, d.Cwd)), danger: true,
		yes: hint{"enter", "delete"}, no: hint{"esc", "keep"},
		run: func(m *Model) tea.Cmd { return m.deleteSession(host, s) },
	}
}

func removeHostDialog(h client.Host) *confirm {
	return &confirm{
		title: "Remove " + h.Name + "?",
		lines: []string{
			fmt.Sprintf("This removes %s from the hosts on this computer.", h.Name),
			"Its sessions and the helper stay on the host, and keep running.",
		},
		danger: true, yes: hint{"enter", "remove"}, no: hint{"esc", "keep"},
		run: func(m *Model) tea.Cmd { return m.saveHosts(h, true) },
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
	det := client.BuildHostDetails(h.host, h.info, len(h.sessions))
	route := det.Target
	if det.SSHConfig != "" {
		route += sFaint.Render("  with " + det.SSHConfig)
	}
	body := []string{"", field("Route", route)}
	if det.Reachable {
		body = append(body,
			field("System", det.System),
			field("Helper", det.Helper))
		// The agents go on more lines rather than lose their versions.
		for n, l := range joinWrap(det.Agents, cw-labelW) {
			k := ""
			if n == 0 {
				k = "Agents"
			}
			body = append(body, field(k, l))
		}
		body = append(body, field("Limits", det.Limits))
	} else {
		msg, _ := errText(h.err)
		if msg == "" {
			msg = "waiting for the host to answer"
		}
		body = append(body, field("Status", sFaint.Render(sentence(msg))))
	}
	body = append(body, "")
	hints := []hint{{"t", "test"}, {"s", "shell"}, {"r", "reconnect"}, {"x", "remove"}, {"esc", "back"}}
	return sheet(w, h.name(), m.hostState(h, w-8-cells(h.name())), hints, trimBlank(body), toneNormal)
}
