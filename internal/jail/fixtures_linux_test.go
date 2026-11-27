package jail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureHome(t *testing.T, populated bool) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if populated {
		if err := os.WriteFile(filepath.Join(home, "fixture"), []byte("home fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestEmptyAndPopulatedHomesAreReadableOnlyOutsideTheJail(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated=%v", populated), func(t *testing.T) {
			in := probeIn{Home: fixtureHome(t, populated)}
			control := unjailed(t, in)
			free, _, _ := start(t, control, in)
			if err := control.Wait(); err != nil || free.Home != "ok" {
				t.Fatalf("the unjailed probe cannot read its home fixture: %v, %q", err, free.Home)
			}
			for _, m := range bothModes {
				t.Run(m.name, func(t *testing.T) {
					inMode(t, m)
					out := jailed(t, Spec{Seccomp: ProfileConverter}, in)
					if out.Home == "ok" {
						t.Error("the jail can read its hidden home fixture")
					}
				})
			}
		})
	}
}

// A file bind needs empty parent directories in the new root. Only paths
// on that exact ancestor chain are allowed. Host siblings must stay hidden.
func namespaceVisibilityIssues(hidden []string, binds []Bind, out probeOut) []string {
	var issues []string
	for _, p := range hidden {
		got := out.Stat[p]
		if probeNeedsAncestor(p, binds) {
			if got != "ok" {
				issues = append(issues, fmt.Sprintf("the jail lacks the required empty ancestor %s: %s", p, got))
			}
		} else if !strings.Contains(got, "no such file") {
			issues = append(issues, fmt.Sprintf("the jail shows the unbound host path %s: %s", p, got))
		}
	}
	return issues
}

func probeNeedsAncestor(p string, binds []Bind) bool {
	for _, b := range binds {
		for parent := filepath.Dir(b.Path); parent != "/"; parent = filepath.Dir(parent) {
			if p == parent {
				return true
			}
		}
	}
	return false
}

func TestNamespaceVisibilityAllowsOnlyTheRequiredEmptyAncestors(t *testing.T) {
	binds := []Bind{{Path: "/var/tmp/bound/probe"}, {Path: "/usr"}}
	hidden := []string{"/var", "/var/tmp", "/var/tmp/bound", "/var/tmp/unbound", "/var/tmp/unbound/secret", "/var/log", "/variance"}
	inside := probeOut{Stat: map[string]string{
		"/var": "ok", "/var/tmp": "ok", "/var/tmp/bound": "ok", "/var/tmp/unbound": "no such file or directory",
		"/var/tmp/unbound/secret": "no such file or directory", "/var/log": "no such file or directory", "/variance": "no such file or directory",
	}}
	if issues := namespaceVisibilityIssues(hidden, binds, inside); len(issues) != 0 {
		t.Fatalf("the empty ancestor skeleton was refused: %v", issues)
	}
	// If a host parent were mounted, its unbound sibling and file would
	// become visible. Keeping the executable reachable cannot excuse that.
	for _, leak := range []string{"/var/tmp/unbound", "/var/tmp/unbound/secret", "/var/log", "/variance"} {
		inside.Stat[leak] = "ok"
		if issues := namespaceVisibilityIssues(hidden, binds, inside); len(issues) != 1 {
			t.Errorf("visible host path %s gave %d issues, want one: %v", leak, len(issues), issues)
		}
		inside.Stat[leak] = "no such file or directory"
	}
}
