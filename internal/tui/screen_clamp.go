package tui

import (
	"context"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	screenChunkSize     = 2048
	maxModeParam        = 65535
	maxScreenWorkBudget = 200_000
)

// onScreenChunk runs before each write chunk after the first. Tests use it to
// cancel a render partway through without racing the scheduler.
var onScreenChunk = func() {}

// ScreenLines renders data through a terminal emulator of cols by rows and
// returns the screen as plain text, one string per row, with trailing spaces
// removed and control or hidden characters cleaned.
func ScreenLines(data []byte, cols, rows int) []string {
	lines, _ := ScreenLinesContext(context.Background(), data, cols, rows)
	return lines
}

// ScreenLinesContext renders data like ScreenLines, stopping early if ctx is
// cancelled.
func ScreenLinesContext(ctx context.Context, data []byte, cols, rows int) (out []string, err error) {
	cols, rows = normalizeScreenSize(cols, rows)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > PeekBytes {
		data = data[len(data)-PeekBytes:]
	}
	defer func() {
		if recover() != nil {
			out, err = make([]string, rows), nil
		}
	}()
	emu, err := emulateScreenContext(ctx, data, cols, rows)
	if err != nil {
		return nil, err
	}
	out = make([]string, rows)
	touched := emu.Touched()
	var b strings.Builder
	for y := range rows {
		if y&31 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if y >= len(touched) || touched[y] == nil {
			continue
		}
		last := cols - 1
		for last >= 0 {
			c := emu.CellAt(last, y)
			if c != nil && c.Content != "" && c.Content != " " {
				break
			}
			last--
		}
		if last < 0 {
			continue
		}
		b.Reset()
		b.Grow(last + 1)
		for x := range last + 1 {
			c := emu.CellAt(x, y)
			switch {
			case c == nil:
				b.WriteByte(' ')
			case c.Content != "":
				b.WriteString(c.Content)
			case c.Width != 0:
				b.WriteByte(' ')
			}
		}
		s := api.CleanText(b.String(), max(cols*4, 4000))
		out[y] = strings.TrimRight(s, " ")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeScreenSize(cols, rows int) (int, int) {
	if cols <= 0 || cols > 1000 {
		cols = 80
	}
	if rows <= 0 || rows > 500 {
		rows = 24
	}
	return cols, rows
}

func emulateScreenContext(ctx context.Context, data []byte, cols, rows int) (*vt.Emulator, error) {
	data, err := clampCSIBytes(ctx, data, cols, rows)
	if err != nil {
		return nil, err
	}
	emu := vt.NewEmulator(cols, rows)
	// charmbracelet/x/vt v0.1.0 ignores SetScrollbackSize(0), keeping 10,000
	// scrollback lines; 1 is the smallest accepted cap.
	emu.SetScrollbackSize(1)
	clampEmulatorCSI(ctx, emu, cols, rows)
	if pw, ok := emu.InputPipe().(io.Closer); ok {
		_ = pw.Close()
	} else {
		data = stripQueries(data)
	}
	if err := writeScreenChunks(ctx, emu, data); err != nil {
		return nil, err
	}
	return emu, nil
}

// renderScreenContext runs bytes through a bounded terminal emulator of the
// session's size, checking ctx between chunks.
func renderScreenContext(ctx context.Context, data []byte, cols, rows int) (shot *screenShot, err error) {
	cols, rows = normalizeScreenSize(cols, rows)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > PeekBytes {
		data = data[len(data)-PeekBytes:]
	}
	defer func() {
		if recover() != nil {
			shot, err = &screenShot{lines: make([]uv.Line, rows), blank: true}, nil
		}
	}()
	emu, err := emulateScreenContext(ctx, data, cols, rows)
	if err != nil {
		return nil, err
	}
	return snapshotEmulator(emu, cols, rows), nil
}

func writeScreenChunks(ctx context.Context, emu *vt.Emulator, data []byte) error {
	for off := 0; off < len(data); off += screenChunkSize {
		if off > 0 {
			onScreenChunk()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(off+screenChunkSize, len(data))
		_, _ = emu.Write(data[off:end])
	}
	return ctx.Err()
}

func snapshotEmulator(emu *vt.Emulator, cols, rows int) *screenShot {
	shot := &screenShot{lines: make([]uv.Line, rows), blank: true}
	for y := range rows {
		line := make(uv.Line, cols)
		nonSpace := false
		for x := range cols {
			if c := emu.CellAt(x, y); c != nil {
				line[x] = *c
				if !nonSpace && c.Content != "" && c.Content != " " && strings.TrimSpace(c.Content) != "" {
					nonSpace = true
				}
			} else {
				line[x] = uv.EmptyCell
			}
		}
		shot.lines[y] = line
		if nonSpace {
			shot.bottom, shot.blank = y, false
		}
	}
	if pos := emu.CursorPosition(); !emu.CursorHidden() && pos.Y > shot.bottom && pos.Y < rows {
		shot.bottom = pos.Y
	}
	return shot
}

var boundedCSICmds = []int{
	'@', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I',
	'L', 'M', 'P', 'S', 'T', 'X', 'Z', '`', 'a', 'd',
	'e', 'f', 'r', 's', ansi.Command(0, ' ', 'q'),
}

func clampEmulatorCSI(ctx context.Context, emu *vt.Emulator, cols, rows int) {
	maxDim := max(cols, rows)
	maxRep := min(cols*(rows+1), 4096)
	workBudget := min(max(cols*rows*2, 4096), maxScreenWorkBudget)
	repBudget := workBudget

	emu.RegisterCsiHandler('b', func(params ansi.Params) bool {
		if ctx.Err() != nil || repBudget <= 0 || workBudget <= 0 {
			return true
		}
		if len(params) == 0 {
			repBudget--
			workBudget--
			return false
		}
		v := params[0].Param(1)
		limit := min(maxRep, repBudget, workBudget)
		if v < 0 || v > limit {
			v = limit
		}
		params[0] = ansi.Param(ansi.Parameter(v, params[0].HasMore()))
		repBudget -= max(v, 1)
		workBudget -= max(v, 1)
		return false
	})

	// Scrolling and line edits can copy the whole screen even for n=1.
	// Character edits can copy one whole row. Charge those actual bounds,
	// rather than only their numeric parameter, against the shared budget.
	for _, cmd := range []int{'S', 'T', 'L', 'M', '@', 'P', 'X'} {
		limit, cost := rows, min(cols*rows, maxScreenWorkBudget)
		if cmd == '@' || cmd == 'P' || cmd == 'X' {
			limit, cost = cols, cols
		}
		emu.RegisterCsiHandler(cmd, func(params ansi.Params) bool {
			if ctx.Err() != nil || workBudget < cost {
				return true
			}
			workBudget -= cost
			if len(params) > 0 {
				v := params[0].Param(1)
				if v < 0 || v > limit {
					params[0] = ansi.Param(ansi.Parameter(limit, params[0].HasMore()))
				}
			}
			return false
		})
	}

	clampDim := func(params ansi.Params) bool {
		if ctx.Err() != nil {
			return true
		}
		for i, p := range params {
			if v := p.Param(0); v < 0 || v > maxDim {
				params[i] = ansi.Param(ansi.Parameter(maxDim, p.HasMore()))
			}
		}
		return false
	}
	for _, cmd := range boundedCSICmds {
		switch cmd {
		case 'S', 'T', 'L', 'M', '@', 'P', 'X':
			continue // the shared work handlers above own these commands
		}
		emu.RegisterCsiHandler(cmd, clampDim)
	}
}

// clampCSIBytes rewrites oversized decimal parameters in CSI sequences before
// vt's parser sees them, preventing integer overflow on huge digit runs while
// leaving SGR colours and terminal modes intact.
func clampCSIBytes(ctx context.Context, data []byte, cols, rows int) ([]byte, error) {
	maxDim := max(cols, rows)
	maxRep := min(cols*(rows+1), 4096)
	var out []byte
	copied := 0
	for i, nextCheck := 0, 0; i < len(data); {
		if i >= nextCheck {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			nextCheck = i + screenChunkSize
		}
		intro := csiIntroLen(data, i)
		if intro == 0 {
			_, size := utf8.DecodeRune(data[i:])
			i += size
			continue
		}
		end, limit := scanCSILimit(data, i+intro, maxDim, maxRep)
		if rewritten, ok := clampParamsSlice(data[i+intro:end], limit); ok {
			if out == nil {
				out = make([]byte, 0, len(data))
			}
			out = append(out, data[copied:i+intro]...)
			out = append(out, rewritten...)
			copied = end
		}
		i = max(end, i+intro)
	}
	if out == nil {
		return data, nil
	}
	return append(out, data[copied:]...), nil
}

func csiIntroLen(data []byte, i int) int {
	if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '[' {
		return 2
	}
	if data[i] == 0x9b {
		return 1
	}
	return 0
}

func scanCSILimit(data []byte, j, maxDim, maxRep int) (end, limit int) {
	if j < len(data) && data[j] >= 0x3c && data[j] <= 0x3f {
		j++
	}
	for j < len(data) && data[j] >= 0x30 && data[j] <= 0x3f {
		j++
	}
	paramEnd := j
	for j < len(data) && data[j] >= 0x20 && data[j] <= 0x2f {
		j++
	}
	if j >= len(data) || data[j] < 0x40 || data[j] > 0x7e {
		// No command has ended this sequence. Keep ordinary parameters
		// intact, bounding only values outside the parser's parameter range.
		return paramEnd, maxModeParam
	}
	switch data[j] {
	case 'm', 'h', 'l':
		return paramEnd, maxModeParam
	case 'b':
		return paramEnd, maxRep
	default:
		return paramEnd, maxDim
	}
}

func clampParamsSlice(params []byte, limit int) ([]byte, bool) {
	changed := false
	for i := 0; i < len(params); {
		if params[i] < '0' || params[i] > '9' {
			i++
			continue
		}
		val, next, over := readDecimal(params, i, limit)
		if over || val > limit {
			changed = true
			break
		}
		i = next
	}
	if !changed {
		return nil, false
	}
	limitBytes := []byte(strconv.Itoa(limit))
	out := make([]byte, 0, len(params))
	for i := 0; i < len(params); {
		if params[i] < '0' || params[i] > '9' {
			out = append(out, params[i])
			i++
			continue
		}
		val, next, over := readDecimal(params, i, limit)
		if over || val > limit {
			out = append(out, limitBytes...)
		} else {
			out = append(out, params[i:next]...)
		}
		i = next
	}
	return out, true
}

func readDecimal(b []byte, i, limit int) (val, next int, over bool) {
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		if !over {
			val = val*10 + int(b[i]-'0')
			if val > limit {
				over = true
			}
		}
		i++
	}
	return val, i, over
}
