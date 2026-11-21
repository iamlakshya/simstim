package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestLockdownOffRequiresItsLiteralWithoutInputBeforeStateOrHosts(t *testing.T) {
	for _, flags := range [][]string{{"--off", "--no-input"}, {"--off", "--confirm=yes", "--no-input"}} {
		t.Run(flags[0]+flags[len(flags)-2], func(t *testing.T) {
			cliPolicyHome(t)
			root := Root()
			root.SetArgs(append([]string{"policy", "lockdown"}, flags...))
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			err := root.ExecuteContext(t.Context())
			var user *client.UserError
			if !errors.As(err, &user) || user.Code != "lockdown_confirm" || user.Kind != client.KindNeedsAnswer {
				t.Fatal("off did not require its own literal", err)
			}
			if _, err := os.Lstat(filepath.Join(client.StateDir(), "policy.lock")); !os.IsNotExist(err) {
				t.Fatal("unconfirmed off created mutation state", err)
			}
		})
	}
}

func TestLockdownIndependentCommandIsNotDisabledByMalformedLocalPolicy(t *testing.T) {
	cliPolicyHome(t)
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte("[broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := Root()
	root.SetArgs([]string{"policy", "lockdown", "--off", "--confirm=lockdown", "--no-input"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(t.Context())
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "no_hosts" {
		t.Fatal("malformed local layer file disabled the independent restriction operation", err)
	}
}
