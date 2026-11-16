package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// addressOf resolves the address of the command line args, as its command
// would: rm and kill strictly. It returns what the lookup said on stderr.
func addressOf(t *testing.T, args ...string) (address, string, error) {
	t.Helper()
	cmd, rest, err := Root().Find(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatal(err)
	}
	cmd.SetContext(withLine(context.Background(), args))
	strict := cmd.Name() == "rm" || cmd.Name() == "kill"
	var a address
	_, errOut := captureOutput(t, func() { a, err = resolveAddress(cmd, cmd.Flags().Arg(0), strict) })
	return a, errOut, err
}

// reportOf is what printError prints for err, and the exit code.
func reportOf(err error) (string, int) {
	var b bytes.Buffer
	code := printError(&b, err, nil, false, false)
	return b.String(), code
}

// failsAs runs the command line, and returns its error as printError
// prints it, and the exit code.
func failsAs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd, err := line(t, args...)
	if err == nil {
		t.Fatalf("%s: no error", strings.Join(args, " "))
	}
	var b bytes.Buffer
	code := printError(&b, err, cmd, false, false)
	return b.String(), code
}

// "-" is the session last attached to, and without one it is not found.
// Only a host that is no longer saved changes the error: a hosts.toml that
// does not decode stays a usage error.
func TestAddressDash(t *testing.T) {
	newTestHome(t)
	fakeBox(t, nil)
	_, _, err := addressOf(t, "attach", "-")
	if got, code := reportOf(err); code != 3 || !strings.Contains(got, "Error: no session has been attached yet.") || !strings.Contains(got, "Try: "+brand.Name+" ls\n") {
		t.Errorf("- with no last session: exit %d\n%s", code, got)
	}
	client.RememberLast("box", "a1b2", "web")
	if a, _, err := addressOf(t, "send", "-", "hi"); err != nil || a.host.Name != "box" || a.ref != "a1b2" {
		t.Errorf("- = %+v, %v", a, err)
	}
	// The last session is gone: the error names it by its name.
	got, code := failsAs(t, "show", "-")
	if code != 3 || !strings.Contains(got, "Error: the last session, web on box, is gone.") || strings.Contains(got, "a1b2") {
		t.Errorf("show - after the session went: exit %d\n%s", code, got)
	}
	client.RememberLast("gone", "a1b2", "web")
	if _, _, err := addressOf(t, "show", "-"); err == nil || !strings.Contains(err.Error(), `"gone", which is no longer a saved host`) {
		t.Errorf("- on a host that was removed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "hosts.toml"), []byte("[[host]]\nname = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = addressOf(t, "show", "-")
	if got, code := reportOf(err); code != 2 || !strings.Contains(got, "hosts.toml") || strings.Contains(got, "no longer a saved host") {
		t.Errorf("- with a hosts.toml that does not decode: exit %d\n%s", code, got)
	}
}

// host/session names its host, and the session part is found on it later.
func TestAddressWithHost(t *testing.T) {
	newTestHome(t)
	fakeBox(t, nil)
	if a, _, err := addressOf(t, "show", "box/auth-fix"); err != nil || a.host.Name != "box" || a.ref != "auth-fix" {
		t.Errorf("box/auth-fix = %+v, %v", a, err)
	}
	_, _, err := addressOf(t, "show", "bx/auth-fix")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_not_found" || exitCode(ue.Kind) != 3 {
		t.Errorf("bx/auth-fix = %v", err)
	}
}

// writeSeen writes the session cache with box's list, seen at the time at.
func writeSeen(t *testing.T, at time.Time, sessions string) {
	t.Helper()
	writeCache(t, fmt.Sprintf(`{"sessions":{"box":%s},"seen":{"box":%q}}`, sessions, at.UTC().Format(time.RFC3339Nano)))
}
