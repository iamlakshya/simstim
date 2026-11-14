package client

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

// TierQuestion asks the user to choose a saved host's first tier. It runs
// without host or file locks. Returning a tier alone does not save it.
type TierQuestion func(context.Context, Host) (Tier, error)

type tierQuestionKey struct{}
type tierLoginPromptKey struct{}

// WithoutTierLoginPrompt keeps automatic tier verification from borrowing
// a launcher's terminal for SSH prompts. Its own tier question still works.
func WithoutTierLoginPrompt(ctx context.Context) context.Context {
	return context.WithValue(ctx, tierLoginPromptKey{}, true)
}

type tierQuestionCall struct {
	done chan struct{}
	err  error
}

type tierQuestions struct {
	ask    TierQuestion
	mu     sync.Mutex
	active map[string]*tierQuestionCall
	turn   chan struct{}
}

// WithTierQuestion supplies the foreground tier question. A nil callback
// keeps unanswered hosts at shared. Concurrent uses join one question.
func WithTierQuestion(ctx context.Context, ask TierQuestion) context.Context {
	turn := make(chan struct{}, 1)
	turn <- struct{}{}
	return context.WithValue(ctx, tierQuestionKey{}, &tierQuestions{ask: ask, active: map[string]*tierQuestionCall{}, turn: turn})
}

// ResolveHostTier reads current policy and, when a foreground question is
// available, saves the user's first answer through the host transaction.
// Background and non-interactive callers keep an unanswered host at shared.
func ResolveHostTier(ctx context.Context, h Host) (tier Tier, err error) {
	ts, saved, err := tierForQuestion(ctx, h)
	if err != nil || !saved {
		return TierShared, err
	}
	if !ts.NeedsAnswer(h.Name) {
		return ts.Of(h.Name), nil
	}
	questions, _ := ctx.Value(tierQuestionKey{}).(*tierQuestions)
	held, _ := ctx.Value(heldKey{h.Name}).(*heldLock)
	if questions == nil || questions.ask == nil || !InputAllowed(ctx) || held != nil && !held.gone.Load() {
		return TierShared, nil
	}
	questions.mu.Lock()
	if call := questions.active[h.Name]; call != nil {
		questions.mu.Unlock()
		select {
		case <-ctx.Done():
			return TierShared, ctx.Err()
		case <-call.done:
			if call.err != nil {
				return TierShared, call.err
			}
			return ResolveHostTier(ctx, h)
		}
	}
	call := &tierQuestionCall{done: make(chan struct{})}
	questions.active[h.Name] = call
	questions.mu.Unlock()
	defer func() {
		questions.mu.Lock()
		call.err = err
		delete(questions.active, h.Name)
		close(call.done)
		questions.mu.Unlock()
	}()
	if err := verifyTierHost(ctx, h); err != nil {
		return TierShared, err
	}
	ts, saved, err = tierForQuestion(ctx, h)
	if err != nil || !saved {
		return TierShared, err
	}
	if !ts.NeedsAnswer(h.Name) {
		return ts.Of(h.Name), nil
	}
	choice, err := questions.choose(ctx, h)
	if err != nil {
		return TierShared, err
	}
	if err := ctx.Err(); err != nil {
		return TierShared, err
	}
	if !choice.Valid() {
		return TierShared, &UserError{Msg: "the tier of " + h.Name + " still needs an answer",
			Hint: brand.Name + " host set " + h.Name + " tier=shared", Code: "needs_answer", Kind: KindNeedsAnswer}
	}
	return saveFirstTierAnswer(ctx, h, ts, choice)
}

// choose serialises terminal questions across hosts too, without retaining
// a policy lock while another host's question is open.
func (q *tierQuestions) choose(ctx context.Context, h Host) (Tier, error) {
	select {
	case <-ctx.Done():
		return TierShared, ctx.Err()
	case <-q.turn:
	}
	defer func() { q.turn <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return TierShared, err
	}
	return q.ask(ctx, h)
}

func tierForQuestion(ctx context.Context, h Host) (Tiers, bool, error) {
	if err := ctx.Err(); err != nil {
		return Tiers{}, false, err
	}
	if h.Name != "" && !ValidHostName(h.Name) {
		return Tiers{}, false, badHostNameError(h.Name)
	}
	hosts, err := LoadHosts()
	if err != nil {
		return Tiers{}, false, err
	}
	i := slices.IndexFunc(hosts, func(now Host) bool { return now.Name == h.Name })
	if i < 0 {
		return Tiers{}, false, nil // a staged host gets its add question
	}
	if !SameEntry(hosts[i], h) {
		return Tiers{}, false, (HostChange{Kind: "the tier question", Was: &h, Now: &h}).changed(&hosts[i])
	}
	ts, err := LoadTiersContext(ctx, hosts)
	return ts, true, err
}

func saveFirstTierAnswer(ctx context.Context, h Host, before Tiers, choice Tier) (Tier, error) {
	ctx, unlock, err := LockHost(ctx, h.Name, "choose the host tier")
	if err != nil {
		return TierShared, err
	}
	defer unlock()
	var pinnedKey string
	if p, err := PinOf(h.Name); err == nil {
		pinnedKey = p.Fingerprint
	}
	err = mutateSavedHostTiers(ctx, h, "choose the host tier", func(ts *Tiers) error {
		if !ts.NeedsAnswer(h.Name) {
			choice = ts.Of(h.Name) // a concurrent explicit answer wins
			return nil
		}
		previous, had := before.Entry(h.Name)
		e, has := ts.Entry(h.Name)
		if had != has || had && !sameTierEntry(previous, e) {
			return &UserError{Msg: fmt.Sprintf("the tier or identity of %s changed while its tier question was open, so that answer was not saved", h.Name),
				Hint: brand.Name + " host show " + h.Name, Code: "host_changed", Kind: KindRefused}
		}
		e.Name, e.Tier, e.SetAt = h.Name, choice, nowUTC()
		e.NeedsAnswer, e.IDChanged = false, false
		if e.Key == "" {
			e.Key = pinnedKey
		}
		ts.entries[h.Name] = e
		return nil
	})
	if err != nil {
		return TierShared, err
	}
	return choice, nil
}

func verifyTierHost(ctx context.Context, h Host) error {
	// This is the first assignment of trust. Check the live pinned host
	// before asking, with this callback suppressed on the verification dial.
	verifyCtx := WithTierQuestion(ctx, nil)
	quietLogin, _ := ctx.Value(tierLoginPromptKey{}).(bool)
	if quietLogin {
		verifyCtx = WithoutInput(verifyCtx)
	}
	conn, err := Dial(verifyCtx, h, !quietLogin)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
