//go:build !windows

package bundle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// fakeSSH is an ssh that answers -G from a config, and writes a log for
// -E, as ssh -v does, then fails to connect. HOMEDIR stands for a home
// folder, which the test fills in, so that no tracked file spells one.
const fakeSSH = `#!/bin/sh
case " $* " in
*" -G "*)
	printf 'hostname 198.51.100.77\nuser zorbo\nidentityfile ~/.ssh/id_zorbo\nproxyjump jumper@hop.example.net:2200\n'
	exit 0 ;;
esac
while [ $# -gt 0 ]; do
	if [ "$1" = -E ]; then log=$2; fi
	shift
done
fp=SHA256:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ
printf 'debug1: Reading configuration data WRAPPER\n' >> "$log"
printf 'debug1: Reading configuration data HOMEDIR/src/acme-infra/ssh/config\n' >> "$log"
printf 'debug1: HOMEDIR/src/acme-infra/ssh/config line 3: Applying options for sshbox\n' >> "$log"
printf 'debug1: Reading configuration data HOMEDIR/.ssh/config\n' >> "$log"
printf 'debug1: kex: algorithm: sntrup761x25519-sha512@openssh.com\n' >> "$log"
printf 'debug1: Executing proxy command: exec cloudflared access ssh --service-token-secret s3cr3t-zz\n' >> "$log"
printf 'debug1: Connecting to 198.51.100.77 [198.51.100.77] port 22.\n' >> "$log"
printf 'debug1: Will attempt key: zorbo@laptop-x ED25519 %s agent\n' "$fp" >> "$log"
printf 'debug1: Offering public key: HOMEDIR/.ssh/id_zorbo ED25519 %s explicit\n' "$fp" >> "$log"
printf 'debug1: Sending env LC_TOKEN = tok-zz-123\n' >> "$log"
printf 'debug1: channel 0: setting env GITHUB_TOKEN = "ghp_zz_456"\n' >> "$log"
printf 'debug1: Authenticating to hop.example.net:2200 as jumper\n' >> "$log"
exit 255
`

// With --ssh, the bundle takes one ssh -v log. What ssh -G says of a host,
// and the keys that ssh -v names, are replaced in it, and the lines that
// can hold an environment variable's value or a proxy's command go.
func TestTheSSHLogLosesWhatSSHKnows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(client.ConfigDir(), "ssh", "config-sshbox")
	script := strings.ReplaceAll(strings.ReplaceAll(fakeSSH, "WRAPPER", wrapper), "HOMEDIR", home)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	h := client.Host{Name: "sshbox", SSH: "sshbox"}
	if err := client.SaveHosts([]client.Host{h}); err != nil {
		t.Fatal(err)
	}
	b := &builder{o: Options{Host: &h, SSH: true}, vals: laptopValues(), res: &Result{}}
	b.configValues(context.Background())
	b.sshFacts(context.Background())
	if b.sshLog == nil {
		t.Fatalf("no ssh -v log: %v", b.res.Notes)
	}
	out := NewScrubber(b.vals).Scrub(b.sshLog)
	for _, bad := range []string{"zorbo", "198.51.100.77", "hop.example.net", "jumper", "SHA256:", "laptop-x", "sshbox",
		"s3cr3t-zz", "tok-zz-123", "ghp_zz_456", "acme-infra", home} {
		if bytes.Contains(out, []byte(bad)) {
			t.Errorf("the log still holds %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"Will attempt key: <key-", "<fingerprint>", "Reading configuration data <ssh-config>",
		"<ssh-config> line 3:", "Reading configuration data ~/.ssh/config", "/ssh/config-<host-", "@openssh.com"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(string(out), "left out by the bundle") != 3 {
		t.Errorf("the lines left out:\n%s", out)
	}
	if k := NewScrubber(b.vals).Check(out); k != "" {
		t.Errorf("the last check finds %s in:\n%s", k, out)
	}
	// A host that a provider made has no SSH log.
	m := client.Host{Name: "boxy", Machine: &client.Machine{Provider: "docker", ID: "c1"}}
	b = &builder{o: Options{Host: &m, SSH: true}, res: &Result{}}
	b.sshFacts(context.Background())
	if b.sshLog != nil || len(b.res.Notes) != 1 || !strings.Contains(b.res.Notes[0], "reached through its provider") {
		t.Errorf("a provider's host: log %q, notes %v", b.sshLog, b.res.Notes)
	}
}
