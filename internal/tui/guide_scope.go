package tui

// guideScope names the modal instance, request and phase that own a result.
// Holding owner keeps its identity stable until the queued reply is discarded.
type guideScope struct {
	owner *guide
	seq   int
	step  gdStep
}

func (g *guide) scope() guideScope     { return guideScope{owner: g, seq: g.seq, step: g.step} }
func (g *guide) nextScope() guideScope { g.seq++; return g.scope() }
func (g *guide) owns(scope guideScope) bool {
	return scope.owner == g && scope.seq == g.seq && scope.step == g.step
}

func (g *guide) finishOp() {
	g.run = false
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
}
