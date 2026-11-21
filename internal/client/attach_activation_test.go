package client

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestAttachArmSkippedQuestionRunnerCannotApproveValues(t *testing.T) {
	f := newArmSourceFixtureV1(t, true)
	called := false
	ctx := WithAttachArmQuestionsV1(f.ctx, func(context.Context, *AttachArmAttemptV1) error { called = true; return nil })
	err := armAttachConnectionV1(ctx, f.conn, f.request, "arm23456", true)
	var user *UserError
	if !called || !errors.As(err, &user) || user.Code != "arm_confirmation" || f.armFrames.Load() != 0 {
		t.Fatal("nil runner result approved an actual arm", err)
	}
	if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("skipped runner ran a credential command", err)
	}
}

func TestAttachArmFinalGateReadsActualOwnerAfterSameRequestQuestions(t *testing.T) {
	f := newArmSourceFixtureV1(t, true)
	var beforeFinal int32
	ctx := WithAttachArmQuestionsV1(f.ctx, func(ctx context.Context, attempt *AttachArmAttemptV1) error {
		if attempt.conn != f.conn || attempt.request != f.request {
			t.Fatal("questions changed the original connection or request")
		}
		err := attempt.Run(ctx)
		confirmed, err := ConfirmGrant(attempt.Context(ctx), err)
		if err != nil {
			return err
		}
		if err := attempt.Run(confirmed); err != nil {
			return err
		}
		beforeFinal = f.snapshots.Load()
		return nil
	})
	if err := armAttachConnectionV1(ctx, f.conn, f.request, "arm23456", true); err != nil {
		t.Fatal(err)
	}
	if f.armFrames.Load() != 1 || f.snapshots.Load() <= beforeFinal {
		t.Fatal("bridge trusted the runner instead of a final actual-owner check")
	}
	if count, err := os.ReadFile(f.marker); err != nil || string(count) != "x" {
		t.Fatal("final arm gate fetched another value", err)
	}
}

func TestAttachArmIgnoredFatalPublicationCannotReplay(t *testing.T) {
	for _, mode := range []string{"partial", "unknown_ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newArmSourceFixtureV1(t, true)
			f.partialArm = mode == "partial"
			f.dropAck = mode == "unknown_ack"
			ctx := WithAttachArmQuestionsV1(f.ctx, func(ctx context.Context, attempt *AttachArmAttemptV1) error {
				err := attempt.Run(ctx)
				confirmed, err := ConfirmGrant(attempt.Context(ctx), err)
				if err != nil {
					return err
				}
				bounded, cancel := context.WithTimeout(confirmed, 200*time.Millisecond)
				defer cancel()
				if err := attempt.Run(bounded); err == nil {
					t.Fatal("uncertain original publication succeeded")
				}
				return nil // A broken runner cannot turn its ignored error into permission.
			})
			if err := armAttachConnectionV1(ctx, f.conn, f.request, "arm23456", true); err == nil || f.armFrames.Load() != 1 {
				t.Fatal("ignored partial/unknown acknowledgement was replayed", err)
			}
		})
	}
}

func TestAttachArmSourceChangeDuringQuestionRefusesBeforeValues(t *testing.T) {
	f := newArmSourceFixtureV1(t, true)
	ctx := WithAttachArmQuestionsV1(f.ctx, func(ctx context.Context, attempt *AttachArmAttemptV1) error {
		err := attempt.Run(ctx)
		confirmed, err := ConfirmGrant(attempt.Context(ctx), err)
		if err != nil {
			return err
		}
		writeRetainedImagePolicyFixture(t, "# original settings changed during the terminal answer\n")
		_ = attempt.Run(confirmed)
		return nil
	})
	if err := armAttachConnectionV1(ctx, f.conn, f.request, "arm23456", true); err == nil || f.armFrames.Load() != 0 {
		t.Fatal("a terminal answer renewed a changed original source", err)
	}
	if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("changed original source ran a credential command", err)
	}
}

func TestAttachArmReconnectNeverCallsQuestionRunner(t *testing.T) {
	f := newArmSourceFixtureV1(t, true)
	called := false
	ctx := WithAttachArmQuestionsV1(f.ctx, func(context.Context, *AttachArmAttemptV1) error { called = true; return nil })
	ctx = quietAttachContext(ctx)
	if err := armAttachConnectionV1(ctx, f.conn, f.request, "arm23456", false); err == nil || called || f.armFrames.Load() != 0 {
		t.Fatal("raw reconnect borrowed terminal questions or answers", err)
	}
	if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unanswered reconnect ran a credential command", err)
	}
}

func TestAttachArmCancelledRunnerCannotReuseItsAttempt(t *testing.T) {
	f := newArmSourceFixtureV1(t, true)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	var retained *AttachArmAttemptV1
	ctx = WithAttachArmQuestionsV1(ctx, func(ctx context.Context, attempt *AttachArmAttemptV1) error {
		retained = attempt
		cancel()
		_ = attempt.Run(ctx)
		return nil
	})
	if err := armAttachConnectionV1(ctx, f.conn, f.request, "arm23456", true); err == nil || f.armFrames.Load() != 0 {
		t.Fatal("cancelled question runner reached a value frame", err)
	}
	if retained == nil || retained.Run(f.ctx) == nil || f.armFrames.Load() != 0 {
		t.Fatal("ended question attempt was revived on a fresh context")
	}
}

func TestAttachArmZeroCapsuleCannotCreateOrRefreshARequest(t *testing.T) {
	var absent *AttachArmAttemptV1
	zero := &AttachArmAttemptV1{}
	for _, attempt := range []*AttachArmAttemptV1{absent, zero} {
		if attempt.Run(t.Context()) == nil {
			t.Fatal("a missing private attempt became an arm operation")
		}
		if got := attempt.Context(t.Context()); got != t.Context() || grantRequestOf(got) != nil {
			t.Fatal("a missing private attempt fabricated a fresh grant request")
		}
	}
}
