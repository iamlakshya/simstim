package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestNewCommandReturnsTheProgramStatusWithoutExtraOutput(t *testing.T) {
	var out bytes.Buffer
	if code := finish(&out, &execOutcome{42, nil}, nil, false, false, false); code != 42 || out.Len() != 0 {
		t.Fatalf("program status=%d output=%q", code, out.String())
	}
}

func TestNewCommandDoesNotHideJoinedCleanupFailure(t *testing.T) {
	var out bytes.Buffer
	cleanup := &client.UserError{Msg: "the original request could not close", Hint: "Check the host before starting more work.", Code: "grant_scope", Kind: client.KindRefused}
	code := finish(&out, errors.Join(&execOutcome{42, nil}, cleanup), nil, false, false, false)
	if code != 42 || !strings.Contains(out.String(), cleanup.Msg) || !strings.Contains(out.String(), "Try: "+cleanup.Hint) {
		t.Fatalf("cleanup failure disappeared: code=%d output=%q", code, out.String())
	}
}
