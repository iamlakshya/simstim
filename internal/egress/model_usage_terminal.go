package egress

// TerminalV1 reports a decoded logical terminal event from this turn's
// forwarded response bytes. It grants no request, reports no usage by itself
// and does not finish the turn or authorise closing another response.
func (t *ModelUsageTurnV1) TerminalV1() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.ignored && t.parser != nil && t.parser.terminal
}
