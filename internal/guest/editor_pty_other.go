//go:build !linux

package guest

import "github.com/iamlakshya/simstim/internal/link"

func (g *Init) serveEditorTerminalV1(stream *link.Stream) { _ = stream.Close() }
