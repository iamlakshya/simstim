package tui

import (
	"image/color"
	"runtime"
	"time"

	"charm.land/lipgloss/v2"
)

// Colour tokens (docs/contributing/ux-spec.md 4.1). Every colour is a palette slot or the
// terminal's own default, so the screens take on the terminal's theme, and
// secondary text is faint (SGR 2). The one accent is cyan, the colour of a
// signal: keys, titles, the selection marker and work in progress. Fixed RGB
// is kept for the brand gradient.
var (
	colAccent  color.Color = lipgloss.Cyan
	colSuccess color.Color = lipgloss.Green
	colWarning color.Color = lipgloss.Yellow
	colDanger  color.Color = lipgloss.Red
	colBrand   color.Color = lipgloss.Cyan // the brand with 16 colours
)

// brandStops is the one fixed gradient, for the mark and the name: deep teal
// to electric cyan. On a light background the teal is deeper, so it keeps its
// contrast. A 256-colour terminal gets the nearest indexes; with 16 colours
// the brand is plain cyan (docs/contributing/ux-spec.md 4.2).
func brandStops(dark bool) []color.Color {
	if dark {
		return []color.Color{lipgloss.Color("#0E7C7B"), lipgloss.Color("#00E5FF")}
	}
	return []color.Color{lipgloss.Color("#045A5A"), lipgloss.Color("#0098B8")}
}

// Styles built from the tokens.
var (
	sPlain   = lipgloss.NewStyle()
	sBold    = lipgloss.NewStyle().Bold(true)
	sFaint   = lipgloss.NewStyle().Faint(true)
	sTitle   = lipgloss.NewStyle().Foreground(colAccent).Bold(true) // a sheet's title
	sKey     = lipgloss.NewStyle().Foreground(colAccent)            // a key in the hints
	sKeyBold = lipgloss.NewStyle().Foreground(colAccent).Bold(true) // a key in a list
	sLink    = lipgloss.NewStyle().Foreground(colAccent)            // what to try next
	sAccent  = lipgloss.NewStyle().Foreground(colAccent)
	sSuccess = lipgloss.NewStyle().Foreground(colSuccess)
	sWarning = lipgloss.NewStyle().Foreground(colWarning)
	sDanger  = lipgloss.NewStyle().Foreground(colDanger)
	sErrHead = lipgloss.NewStyle().Foreground(colDanger).Bold(true)
	sBrand   = lipgloss.NewStyle().Foreground(colBrand)
)

// Glyphs (docs/contributing/ux-spec.md 4.4).
const (
	glyphMarker     = "▍" // the selected row
	glyphNeedsInput = "●"
	glyphOnline     = "●"
	glyphIdle       = "○"
	glyphOK         = "✓"
	glyphFail       = "✕"
	glyphOffline    = "◌"
	glyphInfo       = "•"
	glyphPrompt     = "❯"
	glyphNext       = "→"
	glyphSpinStill  = "⠶"
	sep             = "   " // between hints
	dot             = " · " // between facts
)

// The spinner is a braille scan: one dot running round the cell.
var spinFrames = []string{"⠁", "⠂", "⠄", "⡀", "⢀", "⠠", "⠐", "⠈"}

// optKey names the Alt key the way the platform does.
var optKey = func() string {
	if runtime.GOOS == "darwin" {
		return "opt"
	}
	return "alt"
}()

// Timings.
const (
	spinEvery      = 90 * time.Millisecond
	refreshEvery   = 2 * time.Second  // the lists, and a ping to each host
	latencyEvery   = 10 * time.Second // the round trips' numbers change
	previewDelay   = 120 * time.Millisecond
	activityEvery  = 1500 * time.Millisecond
	toastLife      = 4 * time.Second
	toastErrorLife = 8 * time.Second
	maxBackoff     = 30 * time.Second
)
