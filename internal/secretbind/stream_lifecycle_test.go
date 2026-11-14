package secretbind

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type validationActor struct {
	name   string
	writer io.Writer
	finish func() error
	valid  string
	long   string
	view   any
}

func validationActors(ctx context.Context) []validationActor {
	header := NewHeaderValidator(ctx)
	format := NewFormatValidator(ctx)
	authority := NewAuthorityValidator(ctx)
	return []validationActor{
		{"header", header, header.Finish, "Authorization", strings.Repeat("A", 256), header},
		{"format", format, format.Finish, "basic:account:{value}", "basic:" + strings.Repeat("a", 256) + ":{value}", format},
		{"authority", authority, func() error { _, err := authority.Finish(); return err }, "example.com", strings.Repeat(" ", 256) + "example.com", authority},
	}
}

func TestBindingStreamsCancelBeforeWriteAndFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, actor := range validationActors(ctx) {
		t.Run(actor.name, func(t *testing.T) {
			if n, err := actor.writer.Write([]byte(actor.valid)); n != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled write: count=%d error=%v", n, err)
			}
			if err := actor.finish(); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled finish: %v", err)
			}
		})
	}
	for _, name := range []string{"header", "format", "authority"} {
		t.Run(name+" after input", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for _, actor := range validationActors(ctx) {
				if actor.name != name {
					continue
				}
				if _, err := actor.writer.Write([]byte(actor.valid)); err != nil {
					t.Fatal(err)
				}
				cancel()
				if err := actor.finish(); !errors.Is(err, context.Canceled) {
					t.Fatalf("finish ignored cancellation after input: %v", err)
				}
			}
		})
	}
}

type cancelDuringValidation struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelDuringValidation) Err() error {
	c.checks++
	if c.checks == 17 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestBindingStreamsCancelWithinOneChunk(t *testing.T) {
	for _, name := range []string{"header", "format", "authority"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			counted := &cancelDuringValidation{Context: ctx, cancel: cancel}
			for _, actor := range validationActors(counted) {
				if actor.name != name {
					continue
				}
				n, err := actor.writer.Write([]byte(actor.long))
				if n <= 0 || n >= len(actor.long) || !errors.Is(err, context.Canceled) {
					t.Fatalf("one chunk was not interrupted: count=%d error=%v", n, err)
				}
				if err := actor.finish(); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled stream later finished: %v", err)
				}
			}
		})
	}
}

func TestBindingStreamsCompletionAndInputOwnership(t *testing.T) {
	for _, actor := range validationActors(context.Background()) {
		t.Run(actor.name, func(t *testing.T) {
			input := []byte(actor.valid)
			if n, err := actor.writer.Write(input); n != len(input) || err != nil {
				t.Fatalf("valid write: count=%d error=%v", n, err)
			}
			for i := range input {
				input[i] = '!'
			}
			if err := actor.finish(); err != nil {
				t.Fatalf("validator borrowed the caller's chunk: %v", err)
			}
			if err := actor.finish(); err != nil {
				t.Fatalf("finish was not idempotent: %v", err)
			}
			if n, err := actor.writer.Write([]byte("a")); n != 0 || err == nil {
				t.Fatal("write after Finish was accepted")
			}
			if err := actor.finish(); err != nil {
				t.Fatal("refused post-Finish write changed a completed result")
			}
		})
	}
}

func TestBindingStreamsErrorsStayStickyAndPrivate(t *testing.T) {
	const marker = "private-field-marker"
	header := NewHeaderValidator(context.Background())
	format := NewFormatValidator(context.Background())
	authority := NewAuthorityValidator(context.Background())
	actors := []validationActor{
		{"header", header, header.Finish, marker + "\n", "", header},
		{"format", format, format.Finish, "basic:" + marker + "\u200b:{value}", "", format},
		{"authority", authority, func() error { _, err := authority.Finish(); return err }, marker + ".example:80", "", authority},
	}
	for _, actor := range actors {
		t.Run(actor.name, func(t *testing.T) {
			_, first := actor.writer.Write([]byte(actor.valid))
			if first == nil {
				first = actor.finish()
			}
			if first == nil || strings.Contains(first.Error(), marker) {
				t.Fatal("invalid metadata accepted or echoed")
			}
			if next := actor.finish(); !errors.Is(next, first) {
				t.Fatal("failed stream did not retain its error")
			}
			if n, next := actor.writer.Write([]byte("a")); n != 0 || !errors.Is(next, first) {
				t.Fatal("failed stream accepted more bytes or changed its error")
			}
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
				if got := fmt.Sprintf(verb, actor.view); got != api.Redacted {
					t.Fatal("validator fmt output was not redacted")
				}
			}
		})
	}
}

type contentErrorContext struct{ context.Context }

func (contentErrorContext) Err() error { return errors.New("private-context-marker") }

func TestBindingStreamsDoNotEchoCustomContextErrors(t *testing.T) {
	ctx := contentErrorContext{Context: context.Background()}
	for _, actor := range validationActors(ctx) {
		if _, err := actor.writer.Write([]byte(actor.valid)); err == nil || strings.Contains(err.Error(), "private-context-marker") {
			t.Fatal("custom context error accepted or echoed")
		}
		if err := actor.finish(); err == nil || strings.Contains(err.Error(), "private-context-marker") {
			t.Fatal("Finish accepted or echoed a custom context error")
		}
	}
}

func TestBindingStreamsRefuseMissingContext(t *testing.T) {
	for _, actor := range validationActors(nil) {
		if n, err := actor.writer.Write([]byte(actor.valid)); n != 0 || err == nil {
			t.Fatal("validator without context accepted input")
		}
		if err := actor.finish(); err == nil {
			t.Fatal("validator without context finished")
		}
	}
}
