package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

var wallChoices = []string{"none", "auto", "process", "gvisor", "microvm"}

func (d *newSession) wallKey(key string) {
	step := 0
	switch key {
	case "left", "h":
		step = -1
	case "right", "l", "space":
		step = 1
	}
	if step == 0 {
		return
	}
	for i, value := range wallChoices {
		if value == d.wall || value == "auto" && d.wall == "" {
			d.wall = wallChoices[(i+step+len(wallChoices))%len(wallChoices)]
			d.downgrade = false
			return
		}
	}
}

func (d *newSession) wallRow(m *Model, w int) string {
	var choices []string
	selected := 0
	for _, value := range wallChoices {
		label := value
		if value == "auto" {
			if h := m.hostByName(d.host); h != nil {
				label += " (" + hostWallFloor(h) + ")"
			}
		}
		if value == d.wall || value == "auto" && d.wall == "" {
			selected = len(choices)
			label = sBold.Render("● " + label)
		} else {
			label = sFaint.Render("○ " + label)
		}
		choices = append(choices, label)
	}
	if all := strings.Join(choices, "  "); cells(all) <= w {
		return all
	}
	visible := []string{choices[selected]}
	for distance := 1; distance < len(choices); distance++ {
		if left := selected - distance; left >= 0 {
			next := append([]string{choices[left]}, visible...)
			if cells(strings.Join(next, "  ")) <= w {
				visible = next
			}
		}
		if right := selected + distance; right < len(choices) {
			next := append(append([]string(nil), visible...), choices[right])
			if cells(strings.Join(next, "  ")) <= w {
				visible = next
			}
		}
	}
	return trunc(strings.Join(visible, "  "), w)
}

type downgradeSheet struct {
	closed  bool
	request startReq
	level   string
	floor   string
	typed   input
	wrong   bool
}

func (d *downgradeSheet) paste(text string) {
	if !d.closed {
		d.typed.insert(text)
	}
}

func (d *downgradeSheet) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if m.modal != d || d.closed || m.transferInputBlocked() {
		return nil
	}
	switch k.String() {
	case "esc":
		d.closed = true
		return m.cancelStart(d.request)
	case "enter":
		if d.typed.value() != d.level {
			d.wrong = true
			return nil
		}
		d.closed = true
		d.request.args.Downgrade = true
		if d.request.form != nil {
			d.request.form.downgrade = true
		}
		return m.startAgain(d.request)
	}
	d.typed.key(k)
	return nil
}

func (d *downgradeSheet) view(m *Model) string {
	w := m.dialogWidth()
	cw := w - 4
	body := []string{"", sDanger.Render("Run below the floor of " + shown(d.request.host) + "?"), ""}
	lines := []string{fmt.Sprintf("This session runs at %s, below the %s floor.", d.level, d.floor),
		"A weaker wall protects fewer host resources."}
	if d.level == "none" {
		lines = append(lines, "It has no sandbox. Its files and processes use the host account directly.")
	} else if d.level == "process" {
		lines = append(lines, "It shares your account. Own-tier secrets need an explicit --expose grant.")
	}
	lines = append(lines, "This one start does not change the host's saved floor.")
	for _, line := range lines {
		body = append(body, wrap(line, cw)...)
	}
	body = append(body, "", "Type "+d.level+" to continue:", d.typed.view(cw, true))
	if d.wrong {
		body = append(body, sDanger.Render("Type the level exactly, or press esc to keep the floor."))
	}
	return sheet(w, "Downgrade the wall", "", []hint{{"enter", "confirm level"}, {"esc", "keep floor"}}, trimBlank(body), toneDanger)
}

func downgradeOf(err error) (*client.WallFloorError, bool) {
	var e *client.WallFloorError
	ok := errors.As(err, &e)
	return e, ok
}
