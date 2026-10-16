package bundle_test

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/bundle"
)

// The values of the review's cases, with home folders built at run time
// (scripts/check-private.sh).
func reviewScrubber() *bundle.Scrubber {
	return bundle.NewScrubber(bundle.Values{
		Hosts:    []string{"zorblax", "box.example.org", "qwyn@build-7:22"},
		Laptop:   []string{"quixote-mbp.local", "quixote-mbp"},
		Users:    []string{"qwyn", "alice"},
		Homes:    []string{"/Users" + "/qwyn", "/home" + "/alice", "/var/home" + "/qwyn"},
		Sessions: []string{"acme-fix", "api", "nebula", brand.Name},
		Folders:  []string{"/srv/nebula/app", "/home" + "/qwyn/src/acmecorp", "/srv/clients/globex"},
	})
}

// A user's, a host's or the laptop's name next to a hyphen, a slash or a
// full stop is replaced, or keeps its file out. A session's name there
// stays, as api does in internal/api: such names are often common words.
func TestNamesNextToAHyphenOrASlash(t *testing.T) {
	s := reviewScrubber()
	for in, want := range map[string]string{
		"mount /srv/alice-data ok":                             "mount /srv/<user>-data ok",
		"prompt alice@alice-laptop:~$":                         "prompt <user>@<user>-laptop:~$",
		"host qwyn-desktop":                                    "host <user>-desktop",
		"connect zorblax-2.internal":                           "connect <host-1>-2.internal",
		"connect prod-zorblax":                                 "connect prod-<host-1>",
		"branch alice/fix-login and fix-alice":                 "branch <user>/fix-login and fix-<user>",
		"repo github.com/alice/acme":                           "repo github.com/<user>/acme",
		"open /var/home" + "/qwyn-old/x":                       "open /var/home/<user>-old/x",
		"quixote-mbp-2.local":                                  "<laptop>-2.local",
		"build-7-old":                                          "<host-3>-old",
		"Alice-MacBook-Pro.local":                              "<user>-MacBook-Pro.local",
		"cd ~/alice-notes":                                     "cd ~/<user>-notes",
		"//zorblax-x/share":                                    "//<host-1>-x/share",
		"internal/api/debug.go:12 fix-api nebula-redesign":     "internal/api/debug.go:12 fix-api nebula-redesign",
		"cd /home" + "/qwyn/src/acmecorp-old":                  "cd ~/<folder-2>-old",
		"cd ~/src/acmecorp.git and ~/src/acmecorp_v2":          "cd ~/<folder-2>.git and ~/<folder-2>_v2",
		"cd /srv/clients/globex-2024":                          "cd <folder-3>-2024",
		"read ~/.config/" + brand.Name + "/ssh/config-zorblax": "read ~/.config/" + brand.Name + "/ssh/config-<host-1>",
	} {
		got := string(s.Scrub([]byte(in)))
		if got != want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", in, got, want)
		}
		if k := s.Check([]byte(got)); k != "" {
			t.Errorf("Check(%q) still finds %s", got, k)
		}
	}
	// What the rules leave, the check finds: the file stays out.
	for _, in := range []string{"ssh_zorblax", "x" + "/home" + "/qwyn.bak", "~/src/acmecorp-old"} {
		if s.Check([]byte(in)) == "" {
			t.Errorf("Check(%q) finds nothing", in)
		}
	}
	// A session's name next to a slash or a hyphen passes the check.
	for _, in := range []string{"/srv/nebula-old/app", "/work/nebula", "/srv/nebula/app2"} {
		if k := s.Check([]byte(in)); k != "" {
			t.Errorf("Check(%q) = %s", in, k)
		}
	}
}

// OpenSSH's names with an @ are no email addresses, and the product's own
// name and commands stay, even when a session has the same name.
func TestWordsThatStay(t *testing.T) {
	s := reviewScrubber()
	in := "kex: chacha20-poly1305@openssh.com ssh-ed25519-cert-v01@openssh.com " +
		brand.Name + " doctor, " + brand.HostBinary + " " + brand.VTBinary
	if got := string(s.Scrub([]byte(in))); got != in {
		t.Errorf("Scrub(%q) = %q", in, got)
	}
	if k := s.Check([]byte(in)); k != "" {
		t.Errorf("Check(%q) = %s", in, k)
	}
	if got := string(s.Scrub([]byte("mail qwyn" + "@example.com"))); !strings.Contains(got, "<email>") {
		t.Errorf("an email address stayed: %q", got)
	}
}
