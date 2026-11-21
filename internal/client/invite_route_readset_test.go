package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func routeReadsetHomeV1(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	t.Setenv("USERPROFILE", directory)
	original := systemConfig
	systemConfig = func() string { return filepath.Join(directory, "no-system-config") }
	t.Cleanup(func() { systemConfig = original })
	return directory
}
func TestExplicitRoutePreflightRefusesExecutableBeforeResolver(t *testing.T) {
	home := routeReadsetHomeV1(t)
	for _, directive := range []string{"Match exec touch-marker", "mAtCh=exec touch-marker", "ProxyCommand=touch-marker", "KnownHostsCommand touch-marker", "LocalCommand touch-marker"} {
		t.Run(strings.Fields(directive)[0], func(t *testing.T) {
			path := filepath.Join(home, "config")
			if err := os.WriteFile(path, []byte("Host *\n "+directive+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if source, err := captureExplicitRouteReadsetV1(context.Background(), path); err == nil {
				source.close()
				t.Fatal("executable config passed the pre-resolver boundary")
			}
			if _, err := os.Stat(filepath.Join(home, "touch-marker")); !os.IsNotExist(err) {
				t.Fatal("preflight ran a config command")
			}
		})
	}
}
func TestExplicitRouteIncludeReadsetRejectsEditReplaceAndGlobDrift(t *testing.T) {
	for _, mutation := range []string{"edit", "replace", "glob"} {
		t.Run(mutation, func(t *testing.T) {
			home := routeReadsetHomeV1(t)
			directory := filepath.Join(home, "parts")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			included := filepath.Join(directory, "one.conf")
			if err := os.WriteFile(included, []byte("Host bastion\n HostName bastion.example.com\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "config")
			if err := os.WriteFile(path, []byte("Include "+directory+"/*.conf\nHost target\n ProxyJump bastion\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := captureExplicitRouteReadsetV1(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			if err = source.check(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "edit":
				err = os.WriteFile(included, []byte("Host bastion\n HostName changed.example.com\n"), 0o600)
			case "replace":
				body, e := os.ReadFile(included)
				if e != nil {
					t.Fatal(e)
				}
				replacement := included + ".new"
				if err = os.WriteFile(replacement, body, 0o600); err == nil {
					err = os.Rename(replacement, included)
				}
			case "glob":
				err = os.WriteFile(filepath.Join(directory, "two.conf"), []byte("Host extra\n HostName extra.example.com\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = source.check(context.Background()); err == nil {
				t.Fatal("a changed Include closure remained the original source")
			}
		})
	}
}
func TestExplicitRouteParserHasClosedCompleteSyntax(t *testing.T) {
	for _, line := range []string{`Host="target"`, `Host = target # comment`, `IdentityFile "/path with spaces/key"`} {
		if _, _, err := routeConfigLineV1(line); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range []string{`Include "unfinished`, `Host target\`, `IdentityFile 'ambiguous path'`, `UnknownExecutable thing`} {
		key, args, err := routeConfigLineV1(line)
		if err == nil {
			err = checkRouteConfigDirectiveV1(key, args)
		}
		if err == nil {
			t.Fatalf("incomplete/unknown source syntax passed: %q", line)
		}
	}
}
