package tomlinspect

import "fmt"

// Position follows the pinned byte-offset and one-based line convention.
type Position struct {
	Line, Col, Start, Len int
}

// Error retains a native source position and a redacted/static cause.
type Error struct {
	Position Position
	Cause    error
}

func (e *Error) Error() string { return "invalid TOML at a pinned source position" }
func (e *Error) Unwrap() error { return e.Cause }

func (p Position) String() string { return fmt.Sprintf("line %d, byte %d", p.Line, p.Start) }

type errLexControl struct{ r rune }
type errLexEscape struct{ r rune }
type errLexUTF8 struct{ b byte }
type errLexStringNL struct{}

func (errLexControl) Error() string { return "invalid TOML control character" }
func (errLexEscape) Error() string  { return "invalid TOML string escape" }
func (errLexUTF8) Error() string    { return "invalid TOML UTF8" }
func (errLexStringNL) Error() string {
	return "TOML single-line string contains a newline"
}
