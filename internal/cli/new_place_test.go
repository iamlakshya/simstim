package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func namedPlacementCLIHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	t.Setenv(brand.Env("DEFAULT_HOST"), "")
	hosts := []client.Host{{Name: "box", SSH: "box.example"}, {Name: "other", SSH: "other.example"}}
	if err := client.SaveHosts(t.Context(), hosts); err != nil {
		t.Fatal(err)
	}
	for i, host := range hosts {
		if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, strings.Repeat(string(rune('a'+i)), 32), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.SetDefaultHost(t.Context(), "box"); err != nil {
		t.Fatal(err)
	}
	client.RememberLast("other", "root2345", "last")
}

func TestOptionalNewHostKeepsOriginalCommandDashOffsets(t *testing.T) {
	namedPlacementCLIHome(t)
	for _, test := range []struct {
		args []string
		host string
		dash int
	}{{nil, "box", -1}, {[]string{"-"}, "other", -1}, {[]string{"--", "printf", "literal arg"}, "box", 0}, {[]string{"other", "--", "printf", "literal arg"}, "other", 1}} {
		cmd := newCmd()
		cmd.SetContext(t.Context())
		if err := cmd.ParseFlags(test.args); err != nil {
			t.Fatal(err)
		}
		args := cmd.Flags().Args()
		if err := cmd.Args(cmd, args); err != nil {
			t.Fatal(err)
		}
		host, err := selectedNewHost(cmd, args)
		if err != nil || host.Name != test.host || cmd.ArgsLenAtDash() != test.dash {
			t.Fatal("host resolution changed the original command shape", host.Name, cmd.ArgsLenAtDash(), err)
		}
		if test.dash >= 0 && (len(args[test.dash:]) != 2 || args[test.dash+1] != "literal arg") {
			t.Fatal("host resolution rewrote literal command arguments")
		}
	}
}

func TestOptionalNewHostRejectsExtraWordsAndReservedAutoBeforeDial(t *testing.T) {
	namedPlacementCLIHome(t)
	cmd := newCmd()
	cmd.SetContext(t.Context())
	if err := cmd.ParseFlags([]string{"box", "other"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Args(cmd, cmd.Flags().Args()); err == nil {
		t.Fatal("two host words passed the new grammar")
	}
	if _, err := client.ResolveHost(t.Context(), "auto"); err == nil {
		t.Fatal("a reserved word selected a named host without actual auto facts")
	}
}
