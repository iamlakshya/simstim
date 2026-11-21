package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/join/code"
)

func ownJoinInvite(t *testing.T) (client.HostInvite, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	inv, encoded, err := client.CreateHostInvite(context.Background(), client.InviteOptions{Name: "box", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return inv, encoded
}
func joinedReply(t *testing.T, inv client.HostInvite) string {
	t.Helper()
	text, err := code.EncodeReply(code.Reply{Nonce: inv.Nonce, Account: "dev", Addresses: []string{"192.0.2.2"}, Port: 22, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}})
	if err != nil {
		t.Fatal(err)
	}
	return text
}
func TestJoinPipeNeedsConsentBeforeRoot(t *testing.T) {
	_, encoded := ownJoinInvite(t)
	cmd := hostJoinCmd()
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(encoded))
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.ParseFlags([]string{"--address", "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	err := cmd.RunE(cmd, nil)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer {
		t.Fatalf("join without --yes: %v", err)
	}
}
func TestAcceptPipeNeedsEveryKeyAndTierBeforeConfig(t *testing.T) {
	inv, _ := ownJoinInvite(t)
	encoded := joinedReply(t, inv)
	for _, keys := range [][]string{nil, {"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}} {
		cmd := &cobra.Command{Use: "add"}
		cmd.SetContext(context.Background())
		cmd.SetIn(strings.NewReader(encoded))
		cmd.SetErr(&bytes.Buffer{})
		err := acceptJoinedHost(cmd, "box", "-", "", keys, client.TierUnknown, true)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer {
			t.Fatalf("accept without answers: %v", err)
		}
		if client.HasPin("box") {
			t.Fatal("missing answer pinned a key")
		}
		matches, _ := filepath.Glob(filepath.Join(client.ConfigDir(), "ssh", "joined-*"))
		if len(matches) > 0 {
			t.Fatal("missing answer wrote generated config")
		}
	}
}
func TestDeclinedReplyPinsAndSavesNothing(t *testing.T) {
	inv, _ := ownJoinInvite(t)
	encoded := joinedReply(t, inv)
	var output bytes.Buffer
	original := joinPrompts
	t.Cleanup(func() { joinPrompts = original })
	joinPrompts = func(*cobra.Command) *prompter { return testPrompts(encoded+"\nn\n", &output) }
	cmd := &cobra.Command{Use: "add"}
	cmd.SetContext(context.Background())
	err := acceptJoinedHost(cmd, "box", "-", "", nil, client.TierOwn, true)
	if !isCancel(err) {
		t.Fatalf("declined keys: %v", err)
	}
	if client.HasPin("box") {
		t.Fatal("declined reply pinned a key")
	}
	if _, err = os.Stat(filepath.Join(client.ConfigDir(), "hosts.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("declined reply saved a host")
	}
	if !strings.Contains(output.String(), "SHA256:") {
		t.Fatal("question omitted the fingerprint")
	}
}

func TestLocalJoinEnvelopeIsClosedAndCarriesCodeOnlyAsInput(t *testing.T) {
	options := localJoinOptionsV1{Account: "dev", Addresses: []string{"192.0.2.1"}, Port: 22}
	reader, err := encodeLocalJoinEnvelopeV1("join", "PUBLIC-CODE", options)
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeLocalJoinEnvelopeV1(reader, "join")
	if err != nil || request.Invite != "PUBLIC-CODE" || request.Options.Account != "dev" {
		t.Fatalf("closed envelope: %+v %v", request, err)
	}
	for _, body := range []string{`{"version":2,"kind":"join","options":{"account":"dev","port":22}}`, `{"version":1,"kind":"leave","options":{"account":"dev","port":22}}`, `{"version":1,"kind":"join","options":{"account":"dev","port":22,"manifest":"caller-pins"}}`, strings.Repeat(" ", (16<<10)+1)} {
		if _, err = decodeLocalJoinEnvelopeV1(strings.NewReader(body), "join"); err == nil {
			t.Fatal("unknown version/purpose/pins were accepted by the root input")
		}
	}
}
