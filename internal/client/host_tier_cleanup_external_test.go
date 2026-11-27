package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cli"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestFailedTierCleanupCannotLendAnOrphanTierToHostAdd(t *testing.T) {
	h := client.Host{Name: "box", SSH: "box"}
	ownHosts(t, h)
	client.PinForTest(t, h.Name)
	if err := client.SetTier(h.Name, client.TierOwn, strings.Repeat("1", 32), ""); err != nil {
		t.Fatal(err)
	}
	tierPath := filepath.Join(client.ConfigDir(), "tiers.toml")
	fixture, err := os.ReadFile(tierPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tierPath, append(fixture, []byte("\n  id_changed = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := client.FailHostTierWritesForTest(errors.New("post-commit cleanup failed"))
	defer restore()
	if err := client.ForgetHost(h.Name); err != nil {
		t.Fatal(err)
	}
	restore()
	if _, err := client.FindHost(h.Name); err == nil {
		t.Fatal("host forget did not commit")
	}
	tiers, err := client.LoadTiers(nil)
	if err != nil {
		t.Fatal(err)
	}
	orphan, ok := tiers.Entry(h.Name)
	if !ok || orphan.Tier != client.TierOwn || orphan.HostID == "" || orphan.Key == "" || !orphan.IDChanged {
		t.Fatalf("fixture did not retain the failed cleanup's orphan: %+v", orphan)
	}
	// Even a regression must never contact a host. This private SSH config
	// forces any unexpected connection through a local failing command.
	proxy := "false"
	if runtime.GOOS == "windows" {
		proxy = "cmd /c exit 77"
	}
	config := filepath.Join(t.TempDir(), "ssh-config")
	if err := os.WriteFile(config, []byte("Host *\n    ProxyJump none\n    ProxyCommand "+proxy+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := cli.Root()
	command.SetArgs([]string{"host", "add", h.Name, h.SSH, "--ssh-config", config, "--yes", "--no-input"})
	var user *client.UserError
	if err := command.ExecuteContext(context.Background()); !errors.As(err, &user) || user.Code != "needs_answer" || !strings.Contains(user.Msg, "tier") {
		t.Fatalf("host add inherited the orphan instead of requiring --tier: %v", err)
	}
	if _, err := client.FindHost(h.Name); err == nil {
		t.Fatal("refused host add saved the orphan's name again")
	}
	// A legitimate later save must also discard orphan identity, key and
	// change flags, even when no tier is explicitly supplied to UpdateHosts.
	next := client.Host{Name: h.Name, Machine: &client.Machine{Provider: "docker", ID: "new"}}
	if err := client.UpdateHosts(context.Background(), client.HostChange{Kind: "host add", Now: &next}); err != nil {
		t.Fatal(err)
	}
	tiers, err = client.LoadTiers([]client.Host{next})
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := tiers.Entry(next.Name)
	if entry.Tier != client.TierShared || entry.HostID != "" || entry.Key != "" || entry.IDChanged {
		t.Fatalf("new host inherited orphan metadata: %+v", entry)
	}
}
