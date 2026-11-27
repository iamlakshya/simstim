package egress

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (runtime *classifierRuntime) run(job *classifierJob, originalHost string) {
	s := runtime.server
	job.outcome = classifierUnsure(job.settings, "classify:unsure")
	defer func() {
		job.cancel()
		s.holds.detach(job.ticket)
		s.wakeExpiry()
		runtime.mu.Lock()
		if runtime.jobs[job.key] == job {
			delete(runtime.jobs, job.key)
		}
		runtime.active--
		job.policy = api.Policy{}
		job.binding = classifierGrantBinding{}
		s.flows.remove(job.flow)
		close(job.done)
		runtime.mu.Unlock()
		runtime.wg.Done()
	}()
	// Configured factories are pure. Reject an invalid or untrusted adapter
	// before optional fact preparation can contact a registry or open a file.
	var judge classify.Judge
	var err error
	if runtime.testJudgeFactory != nil {
		judge, err = runtime.testJudgeFactory(job.ctx, job.policy)
	} else {
		judge, err = s.configuredJudge(job.ctx, job.policy)
	}
	if err != nil || judge == nil {
		runtime.fail(job)
		return
	}
	// One original outbound slot covers the complete job, including optional
	// facts and a command. Deadline expiry cannot free it before physical join.
	select {
	case s.originSem <- struct{}{}:
		defer func() { <-s.originSem }()
	case <-job.ctx.Done():
		runtime.fail(job)
		return
	}
	facts, domain := s.collectClassifierFacts(job.ctx, job.binding.request, job.policy)
	if job.ctx.Err() != nil {
		runtime.fail(job)
		return
	}
	if s.policyMu.LockContext(job.ctx) != nil {
		runtime.fail(job)
		return
	}
	if !runtime.currentLocked(job) {
		s.policyMu.Unlock()
		job.outcome.Decision.Rule = "classify:stale"
		return
	}
	input, ok := s.classifierInputLocked(job.binding.source, job.binding.request, originalHost, domain, facts)
	callCtx := s.withJudgeSourceSnapshotLocked(job.ctx)
	s.policyMu.Unlock()
	if !ok {
		job.outcome.Decision.Rule = "classify:source"
		return
	}
	if s.emitClassifierAudit(job.ctx, job.binding.request, "classify:judge-call", "configured optional judge called") != nil {
		runtime.fail(job)
		return
	}
	result, err := judge.Decide(callCtx, input)
	if err == nil {
		result, err = classify.SanitizeResult(callCtx, result, job.settings.maxScope)
	}
	if err != nil || job.ctx.Err() != nil {
		runtime.fail(job)
		return
	}
	if s.policyMu.LockContext(job.ctx) != nil {
		runtime.fail(job)
		return
	}
	if !runtime.currentLocked(job) {
		s.policyMu.Unlock()
		job.outcome.Decision.Rule = "classify:stale"
		return
	}
	result.Reason = cleanAuditText(result.Reason, s.scrubber.snapshot(), 200)
	s.policyMu.Unlock()
	if s.emitClassifierAudit(job.ctx, job.binding.request, "classify:judge-"+result.Action, result.Reason) != nil {
		runtime.fail(job)
		return
	}
	if s.policyMu.LockContext(job.ctx) != nil {
		runtime.fail(job)
		return
	}
	if !runtime.currentLocked(job) {
		s.policyMu.Unlock()
		job.outcome.Decision.Rule = "classify:stale"
		return
	}
	job.outcome.Result = result
	switch result.Action {
	case "allow":
		decision, installErr := s.installClassifierGrantLocked(job.ctx, job.binding, result)
		if installErr != nil {
			var publication *proto.Error
			if result.Scope == "session" && errors.As(installErr, &publication) && publication.Code == "network_publication_required" {
				// The existing supervisor transaction exports this real hold
				// and persists its public rule before Egress can install it.
				proposal, proposalErr := s.classifierSessionProposalLocked(job.binding)
				job.outcome.Decision = Decision{Rule: "classify:session-pending", unmatched: true}
				job.outcome.Pending = true
				s.policyMu.Unlock()
				if proposalErr != nil || s.emitClassifierSessionProposal(job.ctx, proposal) != nil {
					job.outcome.Decision.Rule = "classify:session-unavailable"
					job.outcome.Pending = job.settings.unsure == api.UnknownAsk
				}
				return
			}
			s.policyMu.Unlock()
			runtime.fail(job)
			return
		}
		job.outcome.Decision = decision
		job.outcome.Applied = decision.Allow
		job.outcome.Pending = !decision.Allow && job.settings.unsure == api.UnknownAsk
		if decision.Allow {
			s.holds.answer(job.ticket, s.now())
		}
	case "deny":
		job.outcome.Decision = Decision{Rule: "classify:judge", classifiedRefusal: true}
		job.outcome.Pending = false
		s.holds.noteClassifierRefusal(job.binding.request, s.now())
		s.holds.answer(job.ticket, s.now())
	case "unsure":
		if job.settings.unsure == api.UnknownDeny {
			s.holds.noteClassifierRefusal(job.binding.request, s.now())
			s.holds.answer(job.ticket, s.now())
		}
	}
	s.policyMu.Unlock()
}

func (runtime *classifierRuntime) fail(job *classifierJob) {
	job.outcome = classifierUnsure(job.settings, "classify:judge-unavailable")
	if runtime.server.currentMembers() != job.binding.source || runtime.server.currentEngine() != job.binding.engine {
		job.outcome.Decision.Rule = "classify:stale"
		return
	}
	if _, reason := runtime.server.holds.notice(job.ticket); reason != "" {
		job.outcome.Decision.Rule = "classify:ended"
		return
	}
	// Source retirement and shutdown are not a model outage. A timed-out or
	// malformed configured judge gets the one session notification instead.
	if errors.Is(job.ctx.Err(), context.Canceled) || runtime.ctx.Err() != nil {
		return
	}
	runtime.mu.Lock()
	if runtime.unavailable || runtime.unavailableSending {
		runtime.mu.Unlock()
		return
	}
	runtime.unavailableSending = true
	runtime.mu.Unlock()
	ctx, cancel := context.WithTimeout(runtime.ctx, controlWriteTimeout)
	err := runtime.server.emitJudgeUnavailable(ctx)
	cancel()
	runtime.mu.Lock()
	runtime.unavailableSending = false
	if err == nil {
		runtime.unavailable = true
	}
	runtime.mu.Unlock()
}
