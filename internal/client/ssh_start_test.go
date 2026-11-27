package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func blockingSSHStart(t *testing.T) (Host, string, string) {
	t.Helper()
	dir, h := sshEnvironmentHost(t, TierOwn)
	ready, allow := filepath.Join(dir, "started"), filepath.Join(dir, "allow-exit")
	script := `#!/bin/sh
case " $* " in
*" -G "*) cat "$FAKE_SSH/values"; exit 0;;
*" -O "*) exit 0;;
esac
prev=
for a; do
  if [ "$prev" = -E ]; then printf 'debug1: started\n' > "$a"; fi
  prev=$a
done
touch "$FAKE_SSH/started"
while [ ! -f "$FAKE_SSH/allow-exit" ]; do sleep 0.01; done
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return h, ready, allow
}

func awaitSSHStartFile(t *testing.T, path string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatal("the fake SSH process did not start")
}

func TestSSHStartRunnersReleaseTheHostBeforeWaiting(t *testing.T) {
	for _, runner := range []string{"run", "forward", "debug", "shell", "bridge"} {
		t.Run(runner, func(t *testing.T) {
			h, ready, allow := blockingSSHStart(t)
			s := &SSH{Host: h}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			done, finished := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(finished)
				var err error
				switch runner {
				case "run":
					_, err = s.Run(ctx, "true", nil)
				case "forward":
					err = s.RunForward(ctx, nil, nil, nil, nil, nil)
				case "debug":
					_, err = s.VerboseLog(ctx)
				case "shell":
					err = s.RunShell(ctx, nil, io.Discard, io.Discard)
				case "bridge":
					var p Proc
					p, err = s.Start(ctx, "true")
					if err == nil {
						_ = p.Stdin().Close()
						err = p.Wait()
					}
				}
				done <- err
			}()
			defer func() { cancel(); <-finished }()
			awaitSSHStartFile(t, ready)
			_, unlock, err := LockHost(t.Context(), h.Name, "after SSH start")
			if err != nil {
				t.Fatalf("%s held the host lock during its wait: %v", runner, err)
			}
			unlock()
			if err := os.WriteFile(allow, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", runner, err)
			}
		})
	}
}

func TestSSHStartSharesTheCallersHostLockAndChecksTheSavedEntry(t *testing.T) {
	_, h := sshEnvironmentHost(t, TierOwn)
	s := &SSH{Host: h}
	ctx, outerUnlock, err := LockHost(t.Context(), h.Name, "outer command")
	if err != nil {
		t.Fatal(err)
	}
	outer := sync.OnceFunc(outerUnlock)
	defer outer()
	held, release, err := s.beginSSHStart(ctx, "inner SSH")
	if err != nil {
		t.Fatalf("SSH did not share the caller's lock: %v", err)
	}
	_, nested, err := LockHost(held, h.Name, "nested step")
	if err != nil {
		t.Fatal(err)
	}
	nested()
	release()
	_, foreign, err := LockHost(t.Context(), h.Name, "another command")
	if err == nil {
		foreign()
		t.Fatal("SSH released the caller's host lock")
	}
	outer()
	changed := h
	changed.SSH = "other.example"
	if err := SaveHosts(context.Background(), []Host{changed}); err != nil {
		t.Fatal(err)
	}
	_, release, err = s.beginSSHStart(t.Context(), "SSH")
	if release != nil {
		release()
	}
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "host_changed" {
		t.Fatalf("a stale SSH entry was reused: %v", err)
	}
}

func TestSSHProviderRouteUsesItsSavedOriginForEnvironmentPolicy(t *testing.T) {
	_, h := sshEnvironmentHost(t, TierOwn)
	h.Labels = map[string]string{"provider": "test"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	resolved := Host{Name: h.Name, SSH: h.SSH}
	s := &SSH{Host: resolved, origin: &h}
	ctx, release, err := s.beginSSHStart(t.Context(), "provider SSH")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	args, err := s.connArgs(ctx, false)
	if err != nil {
		t.Fatalf("an own provider's resolved route lost its saved origin: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), wrapperEnvPath(h, false)) {
		t.Fatalf("an own provider was forced to a restricted wrapper: %q", args)
	}
}
