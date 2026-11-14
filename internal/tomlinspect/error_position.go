package tomlinspect

import "errors"

// completeErrorPosition streams the pin's error.go withCol result. Its
// previous-line boundary uses >=, so a next-line first byte intentionally
// follows that exact pinned convention. No lines/input string is built.
func (i *Inspector) completeErrorPosition(err error) {
	var failure *Error
	if !errors.As(err, &failure) || failure.Position.Col != 0 || i.source.ctx == nil || i.source.ctx.Err() != nil {
		return
	}
	start := failure.Position.Start
	if start < 0 {
		failure.Position.Col = 1
		return
	}
	lineStart := 0
	for offset := 0; offset < start-1; offset++ {
		value, err := i.source.at(int64(i.lexer.base + offset))
		if err != nil {
			return
		}
		if value == '\n' {
			lineStart = offset + 1
		}
	}
	failure.Position.Col = max(1, start-lineStart+1)
}
