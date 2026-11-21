package egress

import (
	"context"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

type classifierOutcome struct {
	Result       classify.Result
	WaitExpired  bool
	Applied      bool
	Decision     Decision
	Pending      bool
	WaitDeadline time.Time
}

type classifierSettings struct {
	kind, maxScope string
	timeout        time.Duration
	rate           int
	unsure         api.Unknown
}

func classifierSettingsFor(policy api.Policy) (classifierSettings, error) {
	settings := classifierSettings{kind: "none", maxScope: "session", timeout: 5 * time.Second, rate: 20, unsure: api.UnknownAsk}
	values := policy.Classifier
	if values.Judge != nil {
		settings.kind = *values.Judge
	}
	if values.MaxScope != nil {
		settings.maxScope = *values.MaxScope
	}
	if values.Rate != nil {
		settings.rate = *values.Rate
	}
	if values.Unsure != nil {
		settings.unsure = api.Unknown(*values.Unsure)
	}
	if values.Timeout != nil {
		parsed, err := time.ParseDuration(*values.Timeout)
		if err != nil || parsed <= 0 {
			return settings, badLayerValue("classifier.timeout", "needs a positive duration")
		}
		settings.timeout = parsed
	}
	if settings.kind != "none" && settings.kind != "command" && settings.kind != "model" {
		return settings, badLayerValue("classifier.judge", "needs none, command or model")
	}
	if settings.maxScope != "once" && settings.maxScope != "session" {
		return settings, badLayerValue("classifier.max_scope", "needs once or session")
	}
	if settings.rate < 0 {
		return settings, badLayerValue("classifier.rate", "cannot be negative")
	}
	if settings.unsure != api.UnknownAsk && settings.unsure != api.UnknownDeny {
		return settings, badLayerValue("classifier.unsure", "needs ask or deny")
	}
	return settings, nil
}

type classifierJob struct {
	key      networkHoldKey
	binding  classifierGrantBinding
	ticket   networkHoldTicket
	policy   api.Policy
	settings classifierSettings
	ctx      context.Context
	cancel   context.CancelFunc
	flow     uint64
	done     chan struct{}
	outcome  classifierOutcome // immutable after done closes
}

// This is bounded work and rate state only. The existing Engine, once ledger,
// private target holder and canonical publication callback retain authority.
type classifierRuntime struct {
	server                          *Server
	ctx                             context.Context
	cancel                          context.CancelFunc
	mu                              sync.Mutex
	jobs                            map[networkHoldKey]*classifierJob
	active                          int
	rate                            classifierRate
	unavailable, unavailableSending bool
	closed                          bool
	wg                              sync.WaitGroup
	closeOnce                       sync.Once
	facts                           classifierFacts
	// Tests alone replace the configured adapter, never the grant installer.
	testJudgeFactory func(context.Context, api.Policy) (classify.Judge, error)
}

func newClassifierRuntime(server *Server) *classifierRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &classifierRuntime{server: server, ctx: ctx, cancel: cancel, jobs: make(map[networkHoldKey]*classifierJob)}
}

func (runtime *classifierRuntime) close() {
	runtime.closeOnce.Do(func() {
		runtime.cancel()
		runtime.mu.Lock()
		runtime.closed = true
		for _, job := range runtime.jobs {
			job.cancel()
		}
		runtime.mu.Unlock()
		runtime.wg.Wait()
	})
}

func classifierUnsure(settings classifierSettings, rule string) classifierOutcome {
	return classifierOutcome{Result: classify.Result{Action: "unsure", Scope: settings.maxScope}, Decision: Decision{Rule: rule, unmatched: true}, Pending: settings.unsure == api.UnknownAsk}
}

// runUnknownClassifier is entered outside policyMu only after the hot caller's
// ordinary fixed/stop prechecks. It repeats that read-only eligibility check
// before admitting work. A returned model allow alone never authorises a flow.
func (s *Server) runUnknownClassifier(ctx context.Context, request Request, via, originalHost string) classifierOutcome {
	started := time.Now()
	if ctx == nil || ctx.Err() != nil || s.classifier == nil || s.holds == nil {
		return classifierOutcome{Decision: Decision{Rule: "classify:unavailable"}}
	}
	if s.policyMu.LockContext(ctx) != nil {
		return classifierOutcome{Decision: heldDecision(heldRefusalCancelled)}
	}
	engine, state := s.currentEngine(), s.currentMembers()
	decision := engine.evaluate(request, nil, evalPrecheck)
	if state == nil || engine.unknown != api.UnknownClassify || decision.Allow || !decision.unmatched {
		s.policyMu.Unlock()
		return classifierOutcome{Decision: decision}
	}
	if request.Path != "" {
		barriers, barrierErr := serviceBarriersFor(request)
		if barrierErr != nil {
			s.policyMu.Unlock()
			return classifierOutcome{Decision: Decision{Rule: "classify:unavailable"}}
		}
		for _, barrier := range barriers {
			if barrier.classifier {
				s.holds.noteClassifierRefusal(request, s.now())
				s.policyMu.Unlock()
				return classifierOutcome{Decision: Decision{Rule: "service:path", classifiedRefusal: true}}
			}
		}
	}
	settings, err := classifierSettingsFor(state.metadata.Policy)
	if err != nil {
		s.policyMu.Unlock()
		return classifierOutcome{Decision: Decision{Rule: "classify:configuration"}}
	}
	wait := settings.timeout
	if via == "transparent" {
		wait = min(wait, 3*time.Second)
	} else if via != "explicit" {
		s.policyMu.Unlock()
		return classifierOutcome{Decision: Decision{Rule: "classify:mode"}}
	}
	deadline := started.Add(wait)
	unsure := classifierUnsure(settings, "classify:unsure")
	unsure.WaitDeadline = deadline
	if s.holds.refusedCount(s.now()) >= 5 {
		s.policyMu.Unlock()
		return classifierOutcome{Decision: heldDecision(heldRefusalStopped), WaitDeadline: deadline}
	}
	if settings.kind == "none" {
		s.policyMu.Unlock()
		return unsure
	}
	if _, redacted := publicNetworkHost(s.scrubber, request.Host, originalHost); redacted {
		s.policyMu.Unlock()
		unsure.Decision.Rule = "classify:source"
		return unsure
	}
	preflightFacts := classify.Facts{DomainAge: classify.AgeFact{Status: classify.FactDisabled}, Popularity: classify.RankFact{Status: classify.FactDisabled}}
	if _, ok := s.classifierInputLocked(state, request, originalHost, "", preflightFacts); !ok {
		s.policyMu.Unlock()
		unsure.Decision.Rule = "classify:source"
		return unsure
	}
	runtime := s.classifier
	runtime.mu.Lock()
	key := networkHoldKey{host: request.Host, port: request.Port}
	job := runtime.jobs[key]
	launch := false
	if job != nil && (job.binding.source != state || job.binding.engine != engine) {
		job.cancel()
		delete(runtime.jobs, key)
		job = nil
	}
	if job == nil {
		if runtime.closed || runtime.active >= maxActiveFlows {
			runtime.mu.Unlock()
			s.policyMu.Unlock()
			unsure.Decision.Rule = "classify:pressure"
			return unsure
		}
		if runtime.rate.charge(s.now(), settings.rate) != classifierRateAllowed {
			runtime.mu.Unlock()
			s.policyMu.Unlock()
			unsure.Decision.Rule = "classify:rate"
			return unsure
		}
		holdPolicy, holdErr := holdSettings(state.metadata.Policy)
		if holdErr != nil {
			runtime.mu.Unlock()
			s.policyMu.Unlock()
			unsure.Decision.Rule = "classify:configuration"
			return unsure
		}
		ticket, _, holdErr := s.holds.retain(request, originalHost, s.now(), holdPolicy.open)
		if holdErr != nil {
			runtime.mu.Unlock()
			s.policyMu.Unlock()
			unsure.Decision.Rule = "classify:pressure"
			return unsure
		}
		jobCtx, cancel := context.WithTimeout(runtime.ctx, settings.timeout)
		jobCtx = s.withJudgeSourceSnapshotLocked(jobCtx)
		job = &classifierJob{key: key, ticket: ticket, policy: api.ClonePolicy(state.metadata.Policy), settings: settings, ctx: jobCtx, cancel: cancel, done: make(chan struct{}),
			binding: classifierGrantBinding{source: state, engine: engine, request: request, hold: ticket.entry, expires: ticket.entry.meta.Expires}}
		job.flow = s.flows.addHTTPRequest(activeFlow{request: request, judgeSource: state, secretNames: judgeSourceNames(state), done: job.done, cancel: cancel,
			check: func(_ *Engine, _ *Dialer) bool { return s.currentMembers() == state && s.currentEngine() == engine }})
		if job.flow == 0 {
			cancel()
			s.holds.detach(ticket)
			runtime.mu.Unlock()
			s.policyMu.Unlock()
			unsure.Decision.Rule = "classify:unavailable"
			return unsure
		}
		runtime.jobs[key] = job
		runtime.active++
		runtime.wg.Add(1)
		launch = true
	}
	holdPolicy, holdErr := holdSettings(state.metadata.Policy)
	if holdErr != nil {
		if launch {
			job.cancel()
			delete(runtime.jobs, key)
			runtime.active--
			s.flows.remove(job.flow)
			close(job.done)
			runtime.wg.Done()
		}
		runtime.mu.Unlock()
		s.policyMu.Unlock()
		unsure.Decision.Rule = "classify:configuration"
		return unsure
	}
	callerTicket, _, holdErr := s.holds.attach(request, originalHost, s.now(), holdPolicy.open)
	if holdErr != nil {
		if launch {
			job.cancel()
			delete(runtime.jobs, key)
			runtime.active--
			s.flows.remove(job.flow)
			close(job.done)
			runtime.wg.Done()
		}
		runtime.mu.Unlock()
		s.policyMu.Unlock()
		unsure.Decision.Rule = "classify:pressure"
		return unsure
	}
	runtime.mu.Unlock()
	s.policyMu.Unlock()
	defer func() { s.holds.detach(callerTicket); s.wakeExpiry() }()
	if launch {
		go runtime.run(job, originalHost)
	}
	s.wakeExpiry()
	duration := time.Until(deadline)
	if duration <= 0 {
		unsure.WaitExpired = true
		return unsure
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return classifierOutcome{Decision: heldDecision(heldRefusalCancelled), WaitDeadline: deadline}
	case <-timer.C:
		unsure.WaitExpired = true
		return unsure
	case <-job.done:
		outcome := job.outcome
		outcome.WaitDeadline = deadline
		return outcome
	case <-job.ticket.entry.done:
		_, reason := s.holds.notice(job.ticket)
		if reason != "answered" {
			job.cancel()
			return classifierOutcome{Decision: heldDecision(heldRefusalRefused), WaitDeadline: deadline}
		}
		if s.policyMu.LockContext(ctx) != nil {
			return classifierOutcome{Decision: heldDecision(heldRefusalCancelled), WaitDeadline: deadline}
		}
		current := s.currentEngine().evaluate(request, nil, evalPrecheck)
		s.policyMu.Unlock()
		if current.Allow || !current.unmatched {
			return classifierOutcome{Decision: current, WaitDeadline: deadline}
		}
		select {
		case <-ctx.Done():
			return classifierOutcome{Decision: heldDecision(heldRefusalCancelled), WaitDeadline: deadline}
		case <-timer.C:
			unsure.WaitExpired = true
			return unsure
		case <-job.done:
			outcome := job.outcome
			outcome.WaitDeadline = deadline
			return outcome
		}
	}
}

// The actual validated refuse control path calls this under policyMu before
// ending its existing hold. It cancels work only; it conveys no grant input.
func (runtime *classifierRuntime) cancelHold(id string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for _, job := range runtime.jobs {
		if job.ticket.entry != nil && job.ticket.entry.meta.ID == id {
			job.cancel()
		}
	}
}

func (runtime *classifierRuntime) currentLocked(job *classifierJob) bool {
	s := runtime.server
	if s.currentMembers() != job.binding.source || s.currentEngine() != job.binding.engine || s.currentEngine().unknown != api.UnknownClassify {
		return false
	}
	decision := s.currentEngine().evaluate(job.binding.request, nil, evalPrecheck)
	if decision.Allow || !decision.unmatched {
		return false
	}
	if job.binding.request.Path != "" {
		barriers, err := serviceBarriersFor(job.binding.request)
		if err != nil {
			return false
		}
		for _, barrier := range barriers {
			if barrier.classifier {
				return false
			}
		}
	}
	h := s.holds
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := job.binding.hold
	return !h.closed && entry != nil && h.byID[entry.meta.ID] == entry && entry.reason == "" && entry.meta.Expires.Equal(job.binding.expires) && s.now().Before(job.binding.expires) && entry.request.Host == job.binding.request.Host && entry.request.Port == job.binding.request.Port
}
