package diag

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// CatKey is the attribute that puts a debug line in a category, as in
// log.Debug("reconnected", diag.CatKey, diag.CatNet). A line in a category
// that is off is not written. A line with no category always is: it is
// about debug itself, or about an error.
const CatKey = "cat"

// Lines is a slog.Handler that writes each line at DEBUG and above into a
// File, as a "log" record. Its text is the line as slog's text handler
// writes it, without the time and the level, which the record holds. So a
// value with no text form of its own prints as fmt.Sprint prints it: an
// api.Secret as [redacted]. While it has no file, a Lines discards each
// line at the cost of one atomic load.
type Lines struct {
	s   *lineSink
	h   slog.Handler // the text handler, with the attributes and groups so far
	cat string
}

// lineSink is the writer of a Lines' text handler, shared by the handlers
// that WithAttrs and WithGroup make.
type lineSink struct {
	file atomic.Pointer[File]

	mu sync.Mutex // held around the text handler's one write of each line
	// The current line's, set under mu.
	to    *File
	level slog.Level
	cat   string
}

// NewLines returns a Lines with no file.
func NewLines() *Lines {
	s := &lineSink{}
	h := slog.NewTextHandler(s, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: noTimeOrLevel})
	return &Lines{s: s, h: h}
}

func noTimeOrLevel(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
		return slog.Attr{}
	}
	return a
}

// SetFile sends the lines to f from now on, or discards them when f is nil.
func (l *Lines) SetFile(f *File) { l.s.file.Store(f) }

// Enabled reports whether a line at level would be written.
func (l *Lines) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug && l.s.file.Load() != nil
}

// Handle writes one line, unless its category is off.
func (l *Lines) Handle(ctx context.Context, r slog.Record) error {
	f := l.s.file.Load()
	if f == nil {
		return nil
	}
	cat, found := l.cat, false
	var rest []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == CatKey {
			cat, found = a.Value.String(), true
		} else {
			rest = append(rest, a)
		}
		return true
	})
	if cat != "" && !f.Cats().Has(cat) {
		return nil
	}
	if found {
		r = slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		r.AddAttrs(rest...)
	}
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	l.s.to, l.s.level, l.s.cat = f, r.Level, cat
	if err := l.h.Handle(ctx, r); err != nil {
		return fmt.Errorf("format a debug line: %w", err)
	}
	return nil
}

// WithAttrs returns a Lines whose lines carry as. A CatKey attribute sets
// their category instead.
func (l *Lines) WithAttrs(as []slog.Attr) slog.Handler {
	cat := l.cat
	rest := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		if a.Key == CatKey {
			cat = a.Value.String()
		} else {
			rest = append(rest, a)
		}
	}
	return &Lines{s: l.s, h: l.h.WithAttrs(rest), cat: cat}
}

// WithGroup returns a Lines whose next attributes go in the group name.
func (l *Lines) WithGroup(name string) slog.Handler {
	return &Lines{s: l.s, h: l.h.WithGroup(name), cat: l.cat}
}

// Write takes one line from the text handler, which Handle calls with mu
// held.
func (s *lineSink) Write(p []byte) (int, error) {
	s.to.Add(Record{T: Now(), Cat: s.cat, Ev: EvLog, Level: s.level.String(), Text: strings.TrimSuffix(string(p), "\n")})
	return len(p), nil
}

// Discard is a logger that writes nothing: the debug lines of a process
// whose debug is off.
var Discard = slog.New(slog.DiscardHandler)
