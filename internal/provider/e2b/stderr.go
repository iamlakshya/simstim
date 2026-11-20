package e2b

import "unicode/utf8"

const stderrTailBytes = 8 << 10

func (p *proc) captureStderr(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stderr.Len() < 8192 {
		p.stderr.Write(b)
	}
	if p.runStderr != nil {
		p.runStderr.Write(b)
	}
}

// stderrTail is Run's bounded final diagnostic. Stderr keeps its existing
// head contract for callers that use Start directly.
func (p *proc) stderrTail() string {
	p.mu.Lock()
	if p.runStderr == nil {
		p.mu.Unlock()
		return ""
	}
	b, start := p.runStderr.Tail(stderrTailBytes)
	p.mu.Unlock()
	// A ring wrap can leave the first bytes inside a multibyte rune.
	if start > 0 {
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	return string(b)
}
