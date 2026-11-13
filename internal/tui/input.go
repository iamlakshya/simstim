package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
)

// input is a one-line text field. It draws its own cursor as a reverse-video
// cell, so it needs no blinking and no terminal cursor.
type input struct {
	val         []rune
	pos         int
	placeholder string
}

func newInput(value, placeholder string) input {
	r := []rune(shown(value))
	return input{val: r, pos: len(r), placeholder: shown(placeholder)}
}

func (in *input) value() string { return string(in.val) }

func (in *input) set(v string) {
	in.val = []rune(shown(v))
	in.pos = len(in.val)
}

// insert adds text at the cursor. Control, hidden and bidi characters are
// dropped, and line breaks become spaces, so a paste stays on one safe line.
func (in *input) insert(s string) {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	s = api.CleanText(s, 4000)
	r := []rune(s)
	in.val = slices.Insert(in.val, in.pos, r...)
	in.pos += len(r)
}

// key edits the field. It reports whether the key was used.
func (in *input) key(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "left", "ctrl+b":
		if in.pos > 0 {
			in.pos--
		}
	case "right", "ctrl+f":
		if in.pos < len(in.val) {
			in.pos++
		}
	case "home", "ctrl+a":
		in.pos = 0
	case "end", "ctrl+e":
		in.pos = len(in.val)
	case "backspace", "ctrl+h":
		if in.pos > 0 {
			in.val = slices.Delete(in.val, in.pos-1, in.pos)
			in.pos--
		}
	case "delete", "ctrl+d":
		if in.pos < len(in.val) {
			in.val = slices.Delete(in.val, in.pos, in.pos+1)
		}
	case "ctrl+u":
		in.val = slices.Delete(in.val, 0, in.pos)
		in.pos = 0
	case "ctrl+k":
		in.val = in.val[:in.pos]
	case "ctrl+w", "alt+backspace":
		start := in.pos
		for start > 0 && in.val[start-1] == ' ' {
			start--
		}
		for start > 0 && in.val[start-1] != ' ' && in.val[start-1] != '/' {
			start--
		}
		in.val = slices.Delete(in.val, start, in.pos)
		in.pos = start
	default:
		if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModMeta|tea.ModHyper) != 0 {
			return false
		}
		in.insert(k.Text)
	}
	return true
}

// view draws the field in w cells. A focused field shows its cursor.
func (in *input) view(w int, focused bool) string {
	if w <= 0 {
		return ""
	}
	val := []rune(shown(string(in.val)))
	if len(val) == 0 {
		ph := []rune(trunc(shown(in.placeholder), w))
		if !focused {
			return sFaint.Render(string(ph))
		}
		if len(ph) == 0 {
			return sPlain.Reverse(true).Render(" ")
		}
		return sPlain.Reverse(true).Render(string(ph[:1])) + sFaint.Render(string(ph[1:]))
	}
	pos := min(max(in.pos, 0), len(val))
	start := 0
	if focused && pos >= w {
		start = pos - w + 1
	}
	end := min(len(val), start+w)
	if !focused {
		return string(val[start:end])
	}
	before := string(val[start:pos])
	cur, after := " ", ""
	if pos < len(val) {
		cur = string(val[pos])
		if pos+1 < end {
			after = string(val[pos+1 : end])
		}
	}
	return before + sPlain.Reverse(true).Render(cur) + after
}
