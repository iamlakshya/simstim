//go:build !windows

package bundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// A host's name that is also a word of the records, or of a Go stack,
// leaves the records' keys and the placeholders whole.
func TestAHostNamedLikeAWordLeavesTheRecordsWhole(t *testing.T) {
	rec := `{"t":1,"cat":"runtime","ev":"metrics","rt":{"heap_objects":5,"gc_pauses":1,"sched_p99":3},"mem":{"cpu_some":5,"mem_full":2}}`
	for _, host := range []string{"heap", "cpu", "sched", "full", "objects", "pauses", "ssh"} {
		s := NewScrubber(Values{Hosts: []string{host}})
		if got := string(s.Scrub([]byte(rec))); got != rec {
			t.Errorf("host %s: %s", host, got)
		}
		in := "debug1: Reading configuration data <ssh-config>, then <known-hosts>"
		if got := string(s.Scrub([]byte(in))); got != in {
			t.Errorf("host %s: %s", host, got)
		}
	}
}

// Only the bundle's own placeholders are skipped: any other name between
// < and > is replaced.
func TestOnlyTheBundlesPlaceholdersAreSkipped(t *testing.T) {
	s := NewScrubber(Values{Users: []string{"alice"}, Sessions: []string{"nil"}})
	if got := string(s.Scrub([]byte("owner=<alice> err=<nil>"))); got != "owner=<<user>> err=<<session-1>>" {
		t.Errorf("got %q", got)
	}
}

// In a profile's file names, a user's or the laptop's name keeps the
// profile out unless letters or digits touch it. A host's name there
// passes as a whole part of a path, as a Go package's name does. The
// names of systems and processors in Go's file names stay.
func TestNamesInAProfilesFileNames(t *testing.T) {
	s := NewScrubber(Values{Users: []string{"alice"}, Hosts: []string{"zorblax", "linux", "arm64"}, Laptop: []string{"quixote-mbp"}})
	for f, want := range map[string]string{
		"/srv/alice/x/main.go":                          KindUser,
		"/srv/alice_data/x.go":                          KindUser,
		"/work/quixote-mbp/x.go":                        KindLaptop,
		"/opt/zorblax/build/x.go":                       "",
		"/opt/zorblax-2/x.go":                           KindHost,
		"golang.org/x/sys/unix/zsyscall_linux_arm64.go": "",
		"/srv/malice/x.go":                              "",
	} {
		if got := s.checkPath(f, true); got != want {
			t.Errorf("checkPath(%q) = %q, want %q", f, got, want)
		}
	}
}

// ssh -v names a key's certificate as the key's path and -cert. The key's
// name is replaced there too, and the check finds it if not.
func TestAKeysCertificateLosesItsName(t *testing.T) {
	home := "/Users" + "/alice"
	log := []byte("debug1: identity file " + home + "/.ssh/acme-prod type 3\n" +
		"debug1: identity file " + home + "/.ssh/acme-prod-cert type -1\n" +
		"debug1: Will attempt key: " + home + "/.ssh/acme-prod ED25519 SHA256:abcdefghijABCDEFGHIJabcdefghijABCDEFGHIJabc explicit\n")
	keys := []string{"~/.ssh/acme-prod"}
	for _, m := range keyRe.FindAllSubmatch(log, -1) {
		keys = append(keys, string(m[1]))
	}
	s := NewScrubber(Values{Users: []string{"alice"}, Homes: []string{home}, Keys: keys})
	got := string(s.Scrub(hideConfigPaths(dropSecretLines(log), []string{home})))
	if strings.Contains(got, "acme") || !strings.Contains(got, "<key-1>-cert") {
		t.Errorf("the log:\n%s", got)
	}
	if k := s.Check([]byte("identity file ~/.ssh/acme-prod-cert")); k != KindKey {
		t.Errorf("the check finds %q in a key's certificate", k)
	}
}

// The path of a config file with a space in it goes whole, and so does the
// path of a known_hosts file kept in a project's folder.
func TestTheSSHLogLosesItsConfigPaths(t *testing.T) {
	home := "/Users" + "/alice"
	log := []byte(`debug1: Reading configuration data ` + home + `/work/acme/ssh.conf
debug1: ` + home + `/work/acme/ssh.conf line 3: Applying options for zorblax
debug1: Reading configuration data ` + home + `/My Projects/globex/ssh.conf
debug1: ` + home + `/My Projects/globex/ssh.conf line 3: Applying options for *
debug2: ` + home + `/.ssh/config line 1: Including file ` + home + `/work/hooli conf/x depth 0
debug1: Found key in ` + home + `/work/initech/known_hosts:2
debug1: Found key in ` + home + `/My Stuff/umbrella/known_hosts:2
debug1: Found CA key in ` + home + `/work/vandelay/ssh_hosts:4
debug1: load_hostkeys: fopen ` + home + `/work/vandelay/ssh_hosts: No such file or directory
debug1: ` + home + `/.ssh/config line 2: include ` + home + `/work/wonka/*.conf matched no files
debug1: load_hostkeys: fopen ` + home + `/.ssh/known_hosts2: No such file or directory
debug1: Found key in ` + home + `/.ssh/known_hosts:5
`)
	out := string(hideConfigPaths(dropSecretLines(log), []string{home}))
	for _, bad := range []string{"acme", "globex", "Projects", "hooli", "initech", "Stuff", "umbrella", "vandelay", "wonka"} {
		if strings.Contains(out, bad) {
			t.Errorf("the log holds %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{home + "/.ssh/known_hosts2: No such", home + "/.ssh/config line 1:", "Found key in " + home + "/.ssh/known_hosts:5",
		"Found key in <known-hosts>:2", "Found CA key in <known-hosts>:4", "fopen <known-hosts>: No such", "include <ssh-config> matched no files"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
}

// Only whole lines of a file outside the debug folder go into the bundle:
// what follows the last line break can be anything.
func TestOnlyWholeLinesGoInFromOutside(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	record := `{"t":1,"cat":"net","ev":"ping"}` + "\n"
	outside := filepath.Join(home, "x.jsonl")
	if err := os.WriteFile(outside, []byte(record+"PASSWORD=hunter2 my diary"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := client.DebugDir()
	if err := diag.WriteFile(filepath.Join(dir, diag.OutsideList), []byte(outside+"\n")); err != nil {
		t.Fatal(err)
	}
	b := newTestBuilder(t, Options{})
	if err := b.outsideFiles(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if len(b.a.files) != 1 || b.a.files[0].Name != "laptop/outside/1.jsonl" || b.a.files[0].Size != int64(len(record)) {
		t.Fatalf("the bundle took %+v, want %d bytes", b.a.files, len(record))
	}
}

// A file outside the debug folder goes in only when each whole line is a
// record of the debug mode, with its keys spelled as the debug mode spells
// them and a category that it knows.
func TestOnlyRecordsGoInFromOutside(t *testing.T) {
	for in, want := range map[string]bool{
		`{"T":5,"EV":"login","password":"hunter2"}` + "\nmy diary":                                       false,
		`{"t":1690000000,"ev":"click","user":"x"}` + "\n":                                                false,
		`{"t":1,"cat":"cart","ev":"buy"}` + "\n":                                                         false,
		`{"t":1,"cat":"net","ev":"ping"}` + "\nmy diary\n":                                               false,
		`{"t":1,"cat":"","ev":"start"}` + "\n" + `{"t":2,"cat":"net","ev":"ping"}` + "\n" + `{"t":3,"ca`: true,
		``: false,
	} {
		if got := isRecords([]byte(in)); got != want {
			t.Errorf("isRecords(%q) = %v", in, got)
		}
	}
}

// Why a file outside the debug folder was left out holds no path: the
// path stays in From, which only the command prints.
func TestWhyAnOutsideFileWasLeftOutHoldsNoPath(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "acmecorp-secret-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(proj, "real.jsonl")
	if err := os.WriteFile(real, []byte(`{"t":1,"cat":"net","ev":"ping"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link, hard := filepath.Join(proj, "debug.jsonl"), filepath.Join(proj, "hard.jsonl")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(real, hard); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, hard} {
		_, why := readLaptop(p)
		r := &Result{Left: []Left{{Name: "laptop/outside/1.jsonl", From: p, Why: why}}}
		if txt := string(replacedFile(r, NewScrubber(Values{}))); why == "" || strings.Contains(txt, "acmecorp") {
			t.Errorf("replaced.txt:\n%s", txt)
		}
	}
}
