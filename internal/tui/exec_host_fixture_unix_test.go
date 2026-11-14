//go:build !windows

package tui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// execOwnedBackend gives the UI and canonical client the same saved host.
// Its valid fixture pin uses the canonical exact-host header, file key and
// SSH public-key wire format; Shell must still pass the real pin checks.
func execOwnedBackend(t *testing.T) *fakeBackend {
	t.Helper()
	h := client.Host{Name: "box", SSH: "box"}
	if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
		t.Fatal(err)
	}
	typ := "ssh-ed25519"
	public := make([]byte, 32)
	if _, err := rand.Read(public); err != nil {
		t.Fatal(err)
	}
	blob := binary.BigEndian.AppendUint32(nil, uint32(len(typ)))
	blob = append(blob, typ...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(public)))
	blob = append(blob, public...)
	dir := filepath.Join(client.ConfigDir(), "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "known_hosts-"+client.HostFileKey(h.Name))
	alias := brand.Name + "-" + h.Name
	data := fmt.Sprintf("# %s pin of the host %s\n%s %s %s\n", brand.Name, h.Name, alias, typ, base64.StdEncoding.EncodeToString(blob))
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if pin, err := client.PinOf(h.Name); err != nil || pin.Fingerprint == "" {
		t.Fatalf("fixture pin is not canonical: %+v %v", pin, err)
	}
	if err := client.UpdateHosts(context.Background(), client.HostChange{Kind: "exec fixture", Was: &h, Now: &h, Pin: true, Tier: client.TierOwn}); err != nil {
		t.Fatal(err)
	}
	tiers, err := client.LoadTiersContext(context.Background(), []client.Host{h})
	if err != nil || tiers.NeedsAnswer(h.Name) || tiers.Of(h.Name) != client.TierOwn {
		t.Fatalf("fixture tier: %+v %v", tiers, err)
	}
	t.Setenv("TEST_EXEC_PIN", path)
	t.Setenv("TEST_EXEC_ALIAS", alias)
	t.Cleanup(func() {
		// Canonical forget retires only this physical root's indexed epochs;
		// no test scans, probes or unlinks the shared socket base itself.
		if err := client.ForgetHostContext(context.Background(), h.Name); err != nil {
			t.Error(err)
		}
	})
	be := newFakeBackend()
	be.hosts = []client.Host{h}
	be.state[h.Name] = &fakeHost{}
	be.tiers = map[string]client.TierEntry{h.Name: {Name: h.Name, Tier: client.TierOwn}}
	return be
}

// execSSHFixture preserves real local -G capability queries, which never
// connect. Only the final interactive command is the existing stdio fixture.
func execSSHFixture(t *testing.T, body string) string {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_EXEC_REAL_SSH", realSSH)
	return `case " $* " in
*" -G "*) exec "$TEST_EXEC_REAL_SSH" "$@";;
esac
strict=; pin=; alias=
for arg; do
 case "$arg" in
 StrictHostKeyChecking=yes) strict=yes;;
 UserKnownHostsFile=*) pin=${arg#UserKnownHostsFile=};;
 HostKeyAlias=*) alias=${arg#HostKeyAlias=};;
 esac
done
[ "$strict" = yes ] && [ "$pin" = "$TEST_EXEC_PIN" ] && [ "$alias" = "$TEST_EXEC_ALIAS" ] && [ -r "$pin" ] || exit 82
` + body
}
