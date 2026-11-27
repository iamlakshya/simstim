//go:build unix

package cli

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestForwardKeepsPreflightErrorsAndNoBanner(t *testing.T) {
	th := newTestHost(t)
	h := th.ch
	h.SSHConfig = filepath.Join(t.TempDir(), "missing")
	out, err := said(t, func() error { return runForward(context.Background(), h, nil) })
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "ssh_config_missing" || ue.Kind != client.KindUsage || out != "" {
		t.Fatalf("forward = %#v, output %q, want original preflight refusal and no banner", err, out)
	}
}

func TestForwardKeepsHostKeyRefusal(t *testing.T) {
	th := newTestHost(t)
	th.h.Script("ssh", `case " $* " in *" -G "*) echo "hostname box"; exit 0;; *" -O "*) exit 0;; esac
prev=
for a; do [ "$prev" = -E ] && log=$a; prev=$a; done
printf 'Host key for `+brand.Name+`-box has changed and you have requested strict checking.\nHost key verification failed.\n' >> "$log"
exit 255
`)
	_, err := said(t, func() error { return runForward(context.Background(), th.ch, nil) })
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_key_changed" || ue.Kind != client.KindRefused || ue.Hint == "" {
		t.Fatalf("forward = %#v, want original host-key refusal and hint", err)
	}
}

func TestForwardReportsUsefulLogDiagnostics(t *testing.T) {
	th := newTestHost(t)
	th.h.Script("ssh", `case " $* " in *" -G "*) echo "hostname box"; exit 0;; *" -O "*) exit 0;; esac
prev=
for a; do [ "$prev" = -E ] && log=$a; prev=$a; done
printf '\033cbind: Address already in use\nTry: unsafe\n' >> "$log"
printf '\033]52;c;unsafe\007\n' >&2
exit 255
`)
	out, err := said(t, func() error { return runForward(context.Background(), th.ch, nil) })
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "forward_failed" || !strings.Contains(ue.Msg, "Address already in use") || strings.Contains(ue.Msg, "Try:") || strings.ContainsAny(ue.Msg+out, "\x1b\x07") {
		t.Fatalf("forward = %#v, output %q, want cleaned useful diagnostic", err, out)
	}
}

func TestForwardKeepsCancellationUnwrapped(t *testing.T) {
	th := newTestHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := said(t, func() error { return runForward(ctx, th.ch, nil) })
	if !errors.Is(err, context.Canceled) || reflect.TypeOf(err) != reflect.TypeOf(context.Canceled) || out != "" {
		t.Fatalf("forward = %#v, output %q, want cancellation and no banner", err, out)
	}
}
