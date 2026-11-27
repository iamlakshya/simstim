package classify

import (
	"context"
	"unicode/utf8"
)

type reasonSanitizerKey struct{}

// ReasonSanitizer checks a complete decoded reason against the original
// source. It must redact before changing case, controls or length.
type ReasonSanitizer func(context.Context, string) (string, error)

// WithReasonSanitizer binds the trusted owner's source check to one call.
// It carries no credential lookup or publication capability.
func WithReasonSanitizer(ctx context.Context, sanitize ReasonSanitizer) context.Context {
	if ctx == nil || sanitize == nil {
		return ctx
	}
	return context.WithValue(ctx, reasonSanitizerKey{}, sanitize)
}

func reasonSanitizerOf(ctx context.Context) ReasonSanitizer {
	if ctx == nil {
		return nil
	}
	sanitize, _ := ctx.Value(reasonSanitizerKey{}).(ReasonSanitizer)
	return sanitize
}

// SanitizeResult requires the trusted source hook before any reason
// transformation. Invalid or stale answers return no partial reason.
func SanitizeResult(ctx context.Context, result Result, maxScope string) (Result, error) {
	sanitize := reasonSanitizerOf(ctx)
	if sanitize == nil || ctx.Err() != nil || !utf8.ValidString(result.Reason) {
		return Result{}, errAnswer
	}
	clean, err := sanitize(ctx, result.Reason)
	if err != nil || ctx.Err() != nil {
		return Result{}, errAnswer
	}
	result.Reason = clean
	return BoundResult(result, maxScope)
}

type sourceCheckedJudge struct{ judge Judge }

func requireReasonSanitizer(judge Judge, err error) (Judge, error) {
	if err != nil {
		return nil, err
	}
	return sourceCheckedJudge{judge: judge}, nil
}

func (judge sourceCheckedJudge) Decide(ctx context.Context, input Input) (Result, error) {
	if reasonSanitizerOf(ctx) == nil || ctx.Err() != nil {
		return Result{}, errAnswer
	}
	return judge.judge.Decide(ctx, input)
}
