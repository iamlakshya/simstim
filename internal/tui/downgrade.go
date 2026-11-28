package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/wall"
)

// wallProber is implemented by *client.Conn to probe sandbox levels on hosts
// without a saved wall_floor.
type wallProber interface {
	HasFeature(string) bool
	CapProbe(context.Context, string) (api.CapProbe, error)
}

// wallFloorProber is implemented by test and demo connections to report a
// host's probed wall level without building a full CapProbe.
type wallFloorProber interface {
	ProbeWallFloor(context.Context) (wall.Level, error)
}

func probeConnWall(ctx context.Context, c Conn) (wall.Level, error) {
	raw := unclean(c)
	if fp, ok := raw.(wallFloorProber); ok {
		return fp.ProbeWallFloor(ctx)
	}
	if wp, ok := raw.(wallProber); ok {
		if !wp.HasFeature("sandbox") {
			return wall.None, nil
		}
		p, err := wp.CapProbe(ctx, "")
		if err != nil {
			return wall.None, err
		}
		nowName, _ := api.LevelsFor(p, api.RuntimeStatus{})
		lvl, _ := wall.ParseLevel(nowName)
		return lvl, nil
	}
	return wall.Process, nil
}

// Wall row options in the new-session form (isolation spec 9.4).
var formWallChoices = []string{client.WallAuto, api.LevelProcess, api.LevelGVisor, api.LevelMicroVM, api.LevelNone}

func (d *newSession) wallChoice() string {
	if d.wi <= 0 || d.wi >= len(formWallChoices) {
		return ""
	}
	return formWallChoices[d.wi]
}

func (d *newSession) wallKey(ks string) {
	switch ks {
	case "left", "h":
		if d.wi > 0 {
			d.wi--
		}
	case "right", "l":
		if d.wi < len(formWallChoices)-1 {
			d.wi++
		}
	case "space":
		d.wi = (d.wi + 1) % len(formWallChoices)
	}
}

func (d *newSession) wallChipLabel(choice string) string {
	switch choice {
	case client.WallAuto:
		if d.floor != "" {
			return "auto (" + d.floor + ")"
		}
		return "auto"
	case api.LevelNone:
		return "none…"
	default:
		return choice
	}
}

func (d *newSession) wallRow(w int) string {
	var b strings.Builder
	used := 0
	for i, c := range formWallChoices {
		chip := " " + d.wallChipLabel(c) + " "
		n := cells(chip)
		if used+n > w {
			b.WriteString(sFaint.Render("…"))
			break
		}
		switch {
		case i == d.wi && d.field == 5:
			b.WriteString(listRow(true, seg{chip, sBold}))
		case i == d.wi:
			b.WriteString(sBold.Render(chip))
		default:
			b.WriteString(sFaint.Render(chip))
		}
		used += n
	}
	return b.String()
}

// downgradeInfo describes a wall downgrade awaiting confirmation in the
// launcher's red downgrade sheet (isolation spec 9.4).
type downgradeInfo struct {
	host   string
	floor  string
	from   string
	want   string
	shared bool
}

// downgradeAsk is the red confirmation sheet opened when a new session would
// run below its host's wall floor or without a sandbox. The user must type the
// target level's name before Enter starts the session (isolation spec 9.4).
type downgradeAsk struct {
	startReq
	info     downgradeInfo
	in       input
	mismatch bool
}

func newDowngradeAsk(r startReq, info downgradeInfo) *downgradeAsk {
	return &downgradeAsk{
		startReq: r,
		info:     info,
		in:       newInput("", info.want),
	}
}

func (d *downgradeAsk) paste(s string) {
	d.in.insert(s)
	d.mismatch = false
}

func (d *downgradeAsk) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.cancelStart(d.startReq)
		return nil
	case "enter":
		if strings.TrimSpace(d.in.value()) != d.info.want {
			d.mismatch = true
			return nil
		}
		req := d.startReq
		req.args.Wall = d.info.want
		req.args.Downgrade = true
		req.args.WallFloor = d.info.floor
		req.args.DowngradedFrom = d.info.from
		req.wallResolved = true
		return m.startAgain(req)
	}
	if d.in.key(k) {
		d.mismatch = false
	}
	return nil
}

func (d *downgradeAsk) view(m *Model) string {
	w := m.dialogWidth()
	cw := w - 4
	title := "Run below the floor of " + d.info.host + "?"
	var intro string
	switch {
	case d.info.shared:
		intro = fmt.Sprintf("%s is a shared host (minimum wall: %s). Starting at %s is weaker.", d.info.host, d.info.from, d.info.want)
	case d.info.floor == api.LevelNone || d.info.from == "default":
		title = "Start without a sandbox on " + d.info.host + "?"
		intro = fmt.Sprintf("%s can run only %s. Starting without a sandbox requires confirmation.", d.info.host, d.info.want)
	default:
		intro = fmt.Sprintf("%s has wall floor %s. Starting at %s runs below the floor.", d.info.host, d.info.floor, d.info.want)
	}
	body := []string{""}
	body = append(body, wrap(intro, cw)...)
	body = append(body, "")
	for _, loss := range client.WallDowngradeLosses(d.info.from, d.info.want) {
		for i, line := range wrap(loss, cw-2) {
			lead := sDanger.Render("• ")
			if i > 0 {
				lead = "  "
			}
			body = append(body, lead+line)
		}
	}
	body = append(body,
		"",
		sBold.Render(fmt.Sprintf("Type %q to confirm:", d.info.want)),
		d.in.view(cw, true),
	)
	if d.mismatch {
		body = append(body, sDanger.Render(fmt.Sprintf("Type %q exactly, then press Enter.", d.info.want)))
	}
	body = append(body, "")
	hints := []hint{{"enter", "downgrade"}, {"esc", "cancel"}}
	return sheet(w, title, "", hints, trimBlank(body), toneDanger)
}
