package supervisor

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func newRunToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
func (s *Supervisor) registerLaunch() {
	root := s.owner()
	root.membersMu.Lock()
	defer root.membersMu.Unlock()
	root.mu.Lock()
	stopping := root.stopIntent
	root.mu.Unlock()
	if stopping {
		s.mu.Lock()
		s.stopIntent = true
		s.mu.Unlock()
	}
	if root.launches == nil {
		root.launches = make(map[string]*Supervisor)
	}
	if old := root.launches[s.id]; old != nil && old != s {
		root.memberFault = true
		return
	}
	root.launches[s.id] = s
	if root.expected == nil {
		root.expected = make(map[string]bool)
	}
	root.expected[s.id] = true
}
func (s *Supervisor) failMember() {
	root := s.owner()
	root.membersMu.Lock()
	root.memberFault = true
	root.membersMu.Unlock()
}

// correlatedEmpty is only a reason to await a possible owned natural exit.
// It never establishes that exit, stops a process or manufactures end facts.
func (s *Supervisor) correlatedEmpty() bool { return s.correlatedMembers(false) }

func (s *Supervisor) pendingEmpty() bool { return s.correlatedMembers(true) }

func (s *Supervisor) correlatedMembers(early bool) bool {
	root := s.owner()
	root.membersMu.Lock()
	fault := root.memberFault
	list := make([]*Supervisor, 0, len(root.launches))
	for _, member := range root.launches {
		list = append(list, member)
	}
	generation := root.generation
	expected := len(root.expected)
	root.membersMu.Unlock()
	if fault || generation == "" || len(list) == 0 || len(list) != expected {
		return false
	}
	for _, member := range list {
		member.mu.Lock()
		token := member.runToken
		valid := member.generation == generation && token != "" && member.end.claimed && member.end.token == token
		launched := member.launched
		member.mu.Unlock()
		if early && !valid {
			member.evMu.Lock()
			for _, p := range member.early {
				c, ok := parseEvent(p, member.id, "")
				if ok && c.event == proto.EventEnded && c.runToken == token {
					valid = true
					break
				}
			}
			member.evMu.Unlock()
		}
		if !valid || !early && !launched {
			return false
		}
	}
	return true
}

func (s *Supervisor) naturalEmpty(observedOwnedExit bool, cleanupErr error) bool {
	if !observedOwnedExit || cleanupErr != nil || !s.correlatedEmpty() {
		return false
	}
	runtime, ok := s.sb.(interface{ CleanExit() bool })
	if !ok || !runtime.CleanExit() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settleKills()
	if s.rec.oom || s.rec.oomAgent || s.lost != "" || s.silent || s.captureFailed {
		return false
	}
	if s.capture != nil && s.captureReady != s.capture.Nonce {
		return false
	}
	return true
}

func emptyEndStatus(s *Supervisor) host.Status {
	status := s.claimedStatus()
	status.Since = s.rec.end.At
	return status
}
