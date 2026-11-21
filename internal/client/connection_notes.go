package client

import "context"

// quietConnectionNotes keeps background lock waits off the terminal.
func quietConnectionNotes(ctx context.Context) context.Context {
	return WithNotes(context.WithValue(ctx, noticeKey{}, nil), nil)
}

// quietAttachContext also keeps a reconnect's warnings off the raw terminal.
// The warning still reaches the debug log and the host's saved tier changes.
func quietAttachContext(ctx context.Context) context.Context {
	return WithAttachArmQuestionsV1(WithTierQuestion(WithWarnings(quietConnectionNotes(ctx), nil), nil), nil)
}
