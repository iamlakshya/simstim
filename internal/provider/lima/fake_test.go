package lima

import (
	"context"
	"errors"

	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// fakeLimactl acts as limactl for one Mac or Linux machine. It keeps each
// instance's status, and whether its VM asks for nested virtualisation, in
// files. A start writes ha.pid, as Lima's host agent does, and a stop
// removes it. It logs each call as one line: its arguments, each followed
// by the unit separator, so that the test sees every argument whole.
//
// Files in FAKE_DIR change what it does:
//   - refuse-nesting: this Mac cannot run a VM inside a VM. Every start of
//     a VM that asks for nesting fails, as Lima's does: refuse-stderr goes
//     to stderr, and refuse-ha becomes the instance's ha.stderr.log. With
//     refuse-late, the log is written after the start returns, by a process
//     whose PID is in ha.pid. It removes ha.pid first, as Lima's host agent
//     does.
//   - fail-start: the next start fails: fail-start goes to stderr, and
//     fail-ha becomes the instance's ha.stderr.log.
//   - override-open, override-qemu and override-nested: Lima's
//     override.yaml sets hostLoopback true, vmType qemu, or
//     nestedVirtualization true, whatever create or edit asked for.
//   - edit-fails, delete-fails, version-fails and version-fails-quiet: those
//     commands fail. version holds what --version prints.
//   - after-edit-list-fails, after-edit-list-bad,
//     after-edit-list-incomplete, after-edit-list-empty and
//     after-edit-list-other: once an edit has run, list fails, prints text
//     that is not JSON, leaves out nestedVirtualization, lists nothing, or
//     lists only another instance.
//   - networks-json: the config's networks, as JSON.
//
// Lines that start with # in the copied files are the testdata's notes,
// which limactl never prints.
const fakeLimactl = `#!/bin/sh
FAKE_DIR=${0%/*}
printf '%s\n' "${LIMA_HOME-}" >> "$FAKE_DIR/home-log"
for a; do printf '%s\037' "$a"; done >> "$FAKE_DIR/log"
printf '\n' >> "$FAKE_DIR/log"
cmd=$1; shift
for a; do n=$a; done
copy() { grep -v '^#' "$1"; true; }
case $cmd in
--version)
  if [ -f "$FAKE_DIR/version-fails" ]; then echo "limactl: command failed" >&2; exit 1; fi
  if [ -f "$FAKE_DIR/version-fails-quiet" ]; then exit 3; fi
  if [ -f "$FAKE_DIR/version" ]; then cat "$FAKE_DIR/version"; else echo "limactl version 2.3.0"; fi ;;
list)
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-fails" ]; then echo "fake limactl: list failed" >&2; exit 1; fi
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-bad" ]; then echo '{"name":'; exit 0; fi
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-empty" ]; then exit 0; fi
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-other" ]; then echo '{"name":"other","status":"Running"}'; exit 0; fi
  for f in "$FAKE_DIR"/inst-*; do
    [ -f "$f" ] || continue
    i=${f##*/inst-}
    loop=true; [ -f "$FAKE_DIR/closed-$i" ] && loop=false
    [ -f "$FAKE_DIR/override-open" ] && loop=true
    vm=vz; [ -f "$FAKE_DIR/override-qemu" ] && vm=qemu
    nest=false; if [ -f "$FAKE_DIR/nested-$i" ] || [ -f "$FAKE_DIR/override-nested" ]; then nest=true; fi
    nest="\"nestedVirtualization\":$nest,"
    if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-incomplete" ]; then nest=; fi
    nets=; if [ -f "$FAKE_DIR/networks-json" ]; then nets=",\"networks\":$(cat "$FAKE_DIR/networks-json")"; fi
    listed_home=${LIMA_HOME:-$FAKE_DIR}
    if [ -f "$FAKE_DIR/listed-home" ]; then listed_home=$(cat "$FAKE_DIR/listed-home"); fi
    printf '{"name":"%s","status":"%s","dir":"%s/%s","vmType":"%s","sshConfigFile":"%s/%s/ssh.config","cpus":2,"config":{"plain":true,%s"networkOpts":{"hostLoopback":%s}%s}}\n' \
      "$i" "$(cat "$f")" "$listed_home" "$i" "$vm" "$listed_home" "$i" "$nest" "$loop" "$nets"
  done ;;
create)
  for a; do case $a in --name=*) n=${a#--name=} ;; esac; done
  mkdir -p "${LIMA_HOME:-$FAKE_DIR}/$n"
  printf 'plain: true\n' > "${LIMA_HOME:-$FAKE_DIR}/$n/lima.yaml"
  for a; do case $a in
    '--set=.networkOpts.hostLoopback = false') touch "$FAKE_DIR/closed-$n" ;;
    --nested-virt) touch "$FAKE_DIR/nested-$n" ;;
  esac; done
  echo Stopped > "$FAKE_DIR/inst-$n" ;;
edit)
  if [ -f "$FAKE_DIR/edit-fails" ]; then echo "fake limactl: edit failed" >&2; exit 1; fi
  prev=
  for a; do
    if [ "$prev" = --set ] && [ "$a" = '.nestedVirtualization = false' ]; then rm -f "$FAKE_DIR/nested-$n"; fi
    prev=$a
  done
  touch "$FAKE_DIR/edited" ;;
start)
  d=${LIMA_HOME:-$FAKE_DIR}/$n
  mkdir -p "$d"
  if [ -f "$FAKE_DIR/refuse-nesting" ] && { [ -f "$FAKE_DIR/nested-$n" ] || [ -f "$FAKE_DIR/override-nested" ]; }; then
    if [ -f "$FAKE_DIR/refuse-late" ]; then
      (sleep 0.3; rm -f "$d/ha.pid"; sleep 0.2; copy "$FAKE_DIR/refuse-ha" > "$d/ha.stderr.log") </dev/null >/dev/null 2>&1 &
      echo $! > "$d/ha.pid"
    elif [ -f "$FAKE_DIR/refuse-ha" ]; then
      copy "$FAKE_DIR/refuse-ha" > "$d/ha.stderr.log"
    fi
    copy "$FAKE_DIR/refuse-stderr" >&2
    exit 1
  fi
  if [ -f "$FAKE_DIR/fail-start" ]; then
    if [ -f "$FAKE_DIR/fail-ha" ]; then copy "$FAKE_DIR/fail-ha" > "$d/ha.stderr.log"; fi
    copy "$FAKE_DIR/fail-start" >&2
    rm -f "$FAKE_DIR/fail-start"
    exit 1
  fi
  echo $$ > "$d/ha.pid"
  echo Running > "$FAKE_DIR/inst-$n" ;;
stop) rm -f "$FAKE_DIR/$n/ha.pid"; echo Stopped > "$FAKE_DIR/inst-$n" ;;
delete)
  if [ -f "$FAKE_DIR/delete-fails" ]; then echo "fake limactl: delete failed" >&2; exit 1; fi
  rm -f "$FAKE_DIR/inst-$n" ;;
*) echo "fake limactl: unknown command $cmd" >&2; exit 2 ;;
esac
`

// fakeLima puts the fake limactl first on PATH, sets the operating system
// that the provider sees, and returns the fake's folder.
func fakeLima(t *testing.T, system string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake limactl is a POSIX shell script")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(fakeLimactl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("LIMA_HOME", dir)
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	old := goos
	goos = system
	t.Cleanup(func() { goos = old })
	// Stub only the new VM key check, with no SSH or persisted pin.
	oldPin := pinHostKey
	pinHostKey = func(_ context.Context, h client.Host) (client.Pin, error) {
		if err := os.WriteFile(filepath.Join(dir, "pinned"), []byte(h.Name+" "+h.SSH+" "+h.SSHConfig), 0600); err != nil {
			return client.Pin{}, err
		}
		if _, err := os.Stat(filepath.Join(dir, "pin-fails")); err == nil {
			return client.Pin{}, errors.New("ssh: connect to host 127.0.0.1 port 60022: Connection refused")
		}
		return client.Pin{Fingerprint: "SHA256:" + strings.Repeat("A", 43), Type: "ssh-ed25519", Keys: 1, Source: client.PinFirstUse}, nil
	}
	t.Cleanup(func() { pinHostKey = oldPin })
	return dir
}

// set makes the fake's files: a name alone makes an empty file, and
// name=testdata-file copies that file from testdata.
func set(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		name, from, ok := strings.Cut(f, "=")
		var b []byte
		if ok {
			var err error
			if b, err = os.ReadFile(filepath.Join("testdata", from)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// calls returns the fake's calls, each as its arguments.
func calls(t *testing.T, dir string) [][]string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	var out [][]string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			out = append(out, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
		}
	}
	return out
}

// verbs returns the first argument of each call that changes a VM, in
// order: calls of list and --version are left out.
func verbs(cs [][]string) []string {
	var out []string
	for _, c := range cs {
		if c[0] != "list" && c[0] != "--version" {
			out = append(out, c[0])
		}
	}
	return out
}

// called reports whether the fake was called with exactly args.
func called(cs [][]string, args ...string) bool {
	return slices.ContainsFunc(cs, func(c []string) bool { return slices.Equal(c, args) })
}

// fixtureCreate exercises the real closed creation scope against owned fake
// provider files only; it is not a VM/provider runtime witness.
func fixtureCreate(t *testing.T, ctx context.Context, req provider.Request) (client.Host, error) {
	t.Helper()
	owned, release, err := client.BeginHostCreationContext(ctx, provider.CreationSourceRequest("lima", req))
	if err != nil {
		return client.Host{}, err
	}
	defer release()
	if err := client.CheckHostCreationRemovalContext(owned, req.Name); err != nil {
		return client.Host{}, err
	}
	if err := client.StartHostCreationContext(owned); err != nil {
		return client.Host{}, err
	}
	result, err := Provider{}.Create(owned, req)
	var partial *provider.CreationFailure
	if errors.As(err, &partial) {
		if partial.Host.Name != req.Name || partial.Host.Machine == nil || partial.Host.Machine.Provider != "lima" || partial.Host.Machine.ID != provider.MachineName(req.Name) || partial.Locator == "" {
			t.Fatal("creation failure lost its actual original resource/fate")
		}
		if result.Machine == nil || result.Machine.ID != partial.Host.Machine.ID {
			t.Fatal("partial return differs from preserved original resource")
		}
	}
	return result, err
}

func retainedCreationCause(err error) error {
	var partial *provider.CreationFailure
	if errors.As(err, &partial) {
		return partial.Cause
	}
	return err
}

const retainedHintPrefix = "Inspect the exact VM on its original Lima locator before retrying; "
