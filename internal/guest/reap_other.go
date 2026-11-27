//go:build !linux

package guest

import "log/slog"

// Off Linux there is no child subreaper. The guest init reaches only its
// holder: a process of the sandbox whose parent dies goes to the system's
// init, out of its reach (docs/explanation/architecture.md, 2.5).

// becomeSubreaper reports false: this system has no child subreaper.
func becomeSubreaper(*slog.Logger) bool { return false }

// reapLoop has nothing to reap here.
func (g *Init) reapLoop() {}

// reapOrphans has no orphans to end here.
func (g *Init) reapOrphans() bool { return false }
