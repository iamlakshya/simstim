package egress

import "github.com/iamlakshya/simstim/internal/api"

// Service path fixed denies must see the actual HTTP path before any judge.
// This is local inspection eligibility only, never a network permit. The
// same service data supplies the class; matched denies and stopped classify
// never fall through to this unmatched-host probe.
func (s *Server) classifierServiceInspection(engine *Engine, host string, port int) bool {
	if engine.unknown != api.UnknownClassify || s.holds == nil || s.holds.refusedCount(s.now()) >= 5 {
		return false
	}
	kind, spliceOnly, err := httpClass(host)
	if err != nil || spliceOnly || kind != "service" {
		return false
	}
	decision := engine.evaluate(Request{Host: host, Port: port}, nil, evalPrecheck)
	return decision.unmatched
}
