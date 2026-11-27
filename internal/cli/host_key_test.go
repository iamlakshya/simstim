package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// keyFake is the part of a fake ssh that shows a host key. ssh -G prints
// $HOME/strict as StrictHostKeyChecking. The connection that asks the
// user's settings (LogLevel=DEBUG1) writes $HOME/trust-log, OpenSSH's
// verdict, to the log file that -E names. The connection that reads a key
// (PreferredAuthentications=none) writes $HOME/shows under the alias that
// it was given to the file that UserKnownHostsFile names. With
// $HOME/changed, every other connection fails as ssh does when the pin
// refuses the host's key.
const keyFake = `case " $* " in *" -G "*) printf 'hostname %s\nport 22\nstricthostkeychecking %s\n' "$last" "$(cat "$HOME/strict" 2>/dev/null || echo ask)"; exit 0;; esac
case " $* " in *" LogLevel=DEBUG1 "*) log=/dev/stderr; prev=; for a; do [ "$prev" = -E ] && log=$a; prev=$a; done; cat "$HOME/trust-log" >> "$log" 2>/dev/null; exit 255;; esac
case " $* " in *" PreferredAuthentications=none "*)
  for a; do case $a in UserKnownHostsFile=*) f=${a#UserKnownHostsFile=};; HostKeyAlias=*) alias=${a#HostKeyAlias=};; esac; done
  [ -f "$HOME/shows" ] && echo "$alias $(cat "$HOME/shows")" >> "$f"
  echo "Permission denied (publickey)." >&2
  exit 255;;
esac
if [ -f "$HOME/changed" ]; then log=/dev/stderr; prev=; for a; do [ "$prev" = -E ] && log=$a; prev=$a; done; cat "$HOME/changed" >> "$log"; exit 255; fi
`

// hostKey makes a new Ed25519 host key with random bytes in it, and
// returns it as a known hosts line names it, without the host, and its
// fingerprint.
func hostKey(t *testing.T) (key, fp string) {
	t.Helper()
	r := make([]byte, 32)
	if _, err := rand.Read(r); err != nil {
		t.Fatal(err)
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(len("ssh-ed25519")))
	b = append(b, "ssh-ed25519"...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(r)))
	b = append(b, r...)
	sum := sha256.Sum256(b)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b), "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// hostShows makes the fake host of home show a new key, which the user's
// settings trust, do not know, or name with another key ("trusted",
// "unknown" or "changed"), and returns its fingerprint.
func hostShows(t *testing.T, home, verdict string) string {
	t.Helper()
	key, fp := hostKey(t)
	log := "debug1: Server host key: ssh-ed25519 " + fp + "\r\n"
	switch verdict {
	case "trusted":
		log += "debug1: SSH2_MSG_SERVICE_ACCEPT received\r\ndebug1: Authentications that can continue: publickey\r\nme@box: Permission denied (publickey).\r\n"
	case "unknown":
		log += "No ED25519 host key is known for box and you have requested strict checking.\r\nHost key verification failed.\r\n"
	case "changed":
		log += "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\nHost key verification failed.\r\n"
	}
	for name, body := range map[string]string{"shows": key, "trust-log": log} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fp
}

// pinFor writes a pin of a new key for the host called name, and returns
// its fingerprint.
func pinFor(t *testing.T, name string) string {
	t.Helper()
	key, fp := hostKey(t)
	dir := filepath.Join(client.ConfigDir(), "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts-"+client.HostFileKey(name)), []byte("# "+brand.Name+" pin of the host "+name+"\n"+brand.Name+"-"+name+" "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fp
}

// pinnedAdded is the fingerprint that the pin of the host "added" holds.
func pinnedAdded(t *testing.T) string {
	t.Helper()
	p, err := client.PinOf("added")
	if err != nil {
		t.Fatalf("the pin of added: %v", err)
	}
	return p.Fingerprint
}

// changedLog is what ssh prints when the pin of added refuses a host that
// shows shown.
func changedLog(shown string) string {
	return "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n" +
		"The fingerprint for the ED25519 key sent by the remote host is\r\n" + shown + ".\r\n" +
		"Host key for " + brand.Name + "-added has changed and you have requested strict checking.\r\nHost key verification failed.\r\n"
}

// pinFiles lists the files of the pin of the host "added", staged ones too.
func pinFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	entries, _ := os.ReadDir(filepath.Join(client.ConfigDir(), "ssh"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "known_hosts-"+client.HostFileKey("added")) && !strings.HasSuffix(e.Name(), ".lock") {
			out = append(out, e.Name())
		}
	}
	return out
}

// host rekey of a hand-edited saved name that leads out of the folder is
// refused, with exit code 2, and writes no file.
func TestHostRekeyBadName(t *testing.T) {
	newTestHome(t)
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "../../escape", SSH: "box"}}); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "rekey", "../../escape", "--confirm", "../../escape"); code != 2 || !strings.Contains(got, "cannot name a host") {
		t.Errorf("exit %d\n%s", code, got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(client.ConfigDir())), "escape")); err == nil {
		t.Error("a file was written out of the folder")
	}
}

// host rm of a saved name that cannot name a host forgets it, and touches
// no file: not another host's pin that the name leads to.
func TestHostRmMalformedName(t *testing.T) {
	newTestHome(t)
	victim := pinFor(t, "victim")
	for _, name := range []string{"x/../known_hosts-victim", "../ssh/known_hosts-victim", "a/b"} {
		if err := client.SaveHosts(context.Background(), []client.Host{{Name: name, SSH: "box"}, {Name: "victim", SSH: "victim"}}); err != nil {
			t.Fatal(err)
		}
		okSaid(t, "host", "rm", name)
		if p, err := client.PinOf("victim"); err != nil || p.Fingerprint != victim {
			t.Errorf("host rm %q: victim's pin %+v, %v", name, p, err)
		}
		if _, err := client.FindHost(name); err == nil {
			t.Errorf("host rm %q kept it", name)
		}
	}
}
