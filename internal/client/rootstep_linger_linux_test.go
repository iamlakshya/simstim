//go:build linux

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// These fixtures execute the actual fixed templates against a private normal
// user's filesystem. Root UID/GID checks and service notifications are modelled.
// Native inodes, links, exclusive publication, replacement and parent checks
// are real. They prove no root permission, fsync durability or logind behaviour.
type lingerTemplateFixtureV1 struct {
	t       *testing.T
	root    string
	parent  string
	target  string
	journal string
	events  string
}

func newLingerTemplateFixtureV1(t *testing.T, existingParent bool) *lingerTemplateFixtureV1 {
	t.Helper()
	// A fixed safe prefix lets every absolute system prefix in the source be
	// redirected without weakening its fixed-path production grammar.
	root, err := os.MkdirTemp("/tmp", "linger-origin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &lingerTemplateFixtureV1{t: t, root: root, parent: filepath.Join(root, "var/lib/systemd/linger"), events: filepath.Join(root, "events")}
	f.target = filepath.Join(f.parent, "fixture")
	f.journal = filepath.Join(root, "var/lib", brand.Name, "root-steps/fixture/journal/00000001")
	if err := os.MkdirAll(filepath.Join(root, "var/lib/systemd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if existingParent {
		if err := os.Mkdir(f.parent, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *lingerTemplateFixtureV1) render(name string) string {
	f.t.Helper()
	raw, err := rootTemplates.ReadFile("rootstep/" + name + ".sh")
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.NewReplacer("@BRAND@", brand.Name, "@USER@", "fixture", "@UID@", "1000", "/var", f.root+"/var").Replace(string(raw))
}

func (f *lingerTemplateFixtureV1) run(produce bool, mode string) (string, error) {
	f.t.Helper()
	stat, err := exec.LookPath("stat")
	if err != nil {
		f.t.Fatal(err)
	}
	ln, err := exec.LookPath("ln")
	if err != nil {
		f.t.Fatal(err)
	}
	prefix := `
id() { case "$*" in '-u') printf '0\n';; '-u fixture') printf '1000\n';; *) return 1;; esac; }
chown() { [ "$1" = '0:0' ]; }
sync() { :; }
stat() {
    raw=$("$LINGER_TEST_STAT" "$@") || return 1
    format=
    for argument in "$@"; do case "$argument" in %u|%u:%g:*) format=$argument;; esac; done
    case "$format" in
        %u) printf '0\n';;
        %u:%g:*) tail=${raw#*:}; tail=${tail#*:}; printf '0:0:%s\n' "$tail";;
        *) printf '%s\n' "$raw";;
    esac
}
ln() {
    if [ "$LINGER_TEST_MODE" = race_publish ]; then printf 'foreign\n' > "$LINGER_TEST_TARGET"; fi
    [ "$LINGER_TEST_MODE" != no_hardlinks ] || return 1
    "$LINGER_TEST_LN" "$@" || return 1
    [ "$LINGER_TEST_MODE" != lost_publish_reply ]
}
loginctl() {
    printf '%s %s\n' "$1" "$2" >> "$LINGER_TEST_EVENTS"
    case "$1" in
        enable-linger)
            if [ "$LINGER_TEST_MODE" = replace_enable ]; then
                mv "$LINGER_TEST_TARGET" "$LINGER_TEST_TARGET.replaced"
                printf 'foreign\n' > "$LINGER_TEST_TARGET"
            fi
            [ "$LINGER_TEST_MODE" != fail_enable ]
            ;;
        disable-linger)
            [ "$LINGER_TEST_MODE" != fail_disable ] || return 1
            rm -f -- "$LINGER_TEST_TARGET"
            if [ "$LINGER_TEST_MODE" = reappear_disable ]; then printf 'foreign\n' > "$LINGER_TEST_TARGET"; fi
            ;;
        *) return 1;;
    esac
}
`
	text := f.render("undo")
	if produce {
		text = f.render("common") + "\n" + f.render("linger")
	}
	command := exec.Command("/bin/sh", "-c", prefix+text)
	command.Env = append(os.Environ(), "LINGER_TEST_STAT="+stat, "LINGER_TEST_LN="+ln, "LINGER_TEST_MODE="+mode,
		"LINGER_TEST_TARGET="+f.target, "LINGER_TEST_EVENTS="+f.events)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (f *lingerTemplateFixtureV1) disableCount() int {
	f.t.Helper()
	raw, err := os.ReadFile(f.events)
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	return strings.Count(string(raw), "disable-linger fixture\n")
}

func TestLingerCreatedOriginHasRealFixedUndoAndOwnParentCleanup(t *testing.T) {
	f := newLingerTemplateFixtureV1(t, false)
	if output, err := f.run(true, ""); err != nil {
		t.Fatalf("producer: %v: %s", err, output)
	}
	anchor, err := os.Stat(filepath.Join(f.journal, "linger.origin"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Stat(f.target)
	if err != nil || !os.SameFile(anchor, target) {
		t.Fatal("producer published a copy rather than its original native anchor")
	}
	if output, err := f.run(false, ""); err != nil || !strings.Contains(output, "__ROOT_STEP_UNDONE__") {
		t.Fatalf("native-origin fixed undo: %v: %s", err, output)
	}
	if f.disableCount() != 1 {
		t.Fatal("normal same-origin undo did not issue the fixed disable")
	}
	if _, err := os.Lstat(f.target); !os.IsNotExist(err) {
		t.Fatal("normal undo retained its own linger entry")
	}
	if _, err := os.Lstat(f.parent); !os.IsNotExist(err) {
		t.Fatal("normal undo did not retire its own empty fixed parent")
	}
}

func TestLingerLegacyReplacementAndMissingProofStayLeft(t *testing.T) {
	for _, kind := range []string{"legacy", "replacement", "missing anchor", "parent replacement", "extra alias", "future result", "wrong generation"} {
		t.Run(kind, func(t *testing.T) {
			f := newLingerTemplateFixtureV1(t, true)
			if kind == "legacy" {
				if err := os.WriteFile(f.target, []byte("legacy\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if output, err := f.run(true, ""); err != nil {
				t.Fatalf("producer: %v: %s", err, output)
			}
			switch kind {
			case "replacement":
				if err := os.Rename(f.target, f.target+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.target, []byte("foreign\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing anchor":
				if err := os.Remove(filepath.Join(f.journal, "linger.origin")); err != nil {
					t.Fatal(err)
				}
			case "parent replacement":
				if err := os.Rename(f.parent, f.parent+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.parent, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.target, []byte("foreign\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "extra alias":
				if err := os.Link(f.target, filepath.Join(f.root, "extra")); err != nil {
					t.Fatal(err)
				}
			case "future result", "wrong generation":
				name := filepath.Join(f.journal, "linger.result")
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				fields := strings.Fields(string(raw))
				if kind == "future result" {
					fields[0] = "2"
				} else {
					fields[1] = strings.Repeat("f", 32)
				}
				if err := os.WriteFile(name, []byte(strings.Join(fields, " ")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(f.target)
			if err != nil {
				t.Fatal(err)
			}
			if output, err := f.run(false, ""); err != nil || !strings.Contains(output, "LEFT linger") || !strings.Contains(output, "__ROOT_STEP_LEFT__") {
				t.Fatalf("preserve unproven origin: %v: %s", err, output)
			}
			after, err := os.Lstat(f.target)
			if err != nil || !os.SameFile(before, after) || f.disableCount() != 0 {
				t.Fatal("unproven or foreign linger was disabled, replaced or deleted")
			}
			if _, err := os.Stat(f.journal); err != nil {
				t.Fatal("Left discarded its evidence:", err)
			}
		})
	}
}

func TestLingerExclusivePublicationAndLostResultRecovery(t *testing.T) {
	for _, mode := range []string{"race_publish", "no_hardlinks", "lost_publish_reply", "fail_enable"} {
		t.Run(mode, func(t *testing.T) {
			f := newLingerTemplateFixtureV1(t, true)
			if _, err := f.run(true, mode); err == nil {
				t.Fatal("interrupted producer reported success")
			}
			output, err := f.run(false, "")
			if err != nil {
				t.Fatalf("recovery: %v: %s", err, output)
			}
			if mode == "race_publish" {
				if f.disableCount() != 0 || !strings.Contains(output, "LEFT linger") {
					t.Fatal("a raced destination was adopted")
				}
				raw, err := os.ReadFile(f.target)
				if err != nil || string(raw) != "foreign\n" {
					t.Fatal("raced target was changed")
				}
			} else if mode == "no_hardlinks" {
				if f.disableCount() != 0 {
					t.Fatal("unpublished anchor caused a disable")
				}
			} else if f.disableCount() != 1 {
				t.Fatal("fresh native anchor did not recover its own publication")
			}
		})
	}
}

func TestLingerFailedAndChangedPostcheckNeverUnlinksAgain(t *testing.T) {
	for _, mode := range []string{"fail_disable", "reappear_disable"} {
		t.Run(mode, func(t *testing.T) {
			f := newLingerTemplateFixtureV1(t, true)
			if output, err := f.run(true, ""); err != nil {
				t.Fatalf("producer: %v: %s", err, output)
			}
			if output, err := f.run(false, mode); err != nil || !strings.Contains(output, "LEFT linger") {
				t.Fatalf("postcheck: %v: %s", err, output)
			}
			if _, err := os.Stat(f.target); err != nil || f.disableCount() != 1 {
				t.Fatal("failed or replaced postcheck triggered a raw unlink/retry")
			}
			if _, err := os.Stat(f.journal); err != nil {
				t.Fatal("postcheck Left discarded its original evidence")
			}
		})
	}
}

func TestLingerAlreadyAbsentDoesNotDisableAnotherState(t *testing.T) {
	f := newLingerTemplateFixtureV1(t, true)
	if output, err := f.run(true, ""); err != nil {
		t.Fatalf("producer: %v: %s", err, output)
	}
	if err := os.Remove(f.target); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(false, ""); err != nil || !strings.Contains(output, "__ROOT_STEP_UNDONE__") {
		t.Fatalf("already absent: %v: %s", err, output)
	}
	if f.disableCount() != 0 {
		t.Fatal("absence was promoted into a disable permission")
	}
	if _, err := os.Stat(f.parent); err != nil {
		t.Fatal("undo removed a pre-existing parent")
	}
}

func TestLingerCreatedParentWithAnotherUsersEntryStaysLeft(t *testing.T) {
	f := newLingerTemplateFixtureV1(t, false)
	if output, err := f.run(true, ""); err != nil {
		t.Fatalf("producer: %v: %s", err, output)
	}
	other := filepath.Join(f.parent, "another")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(false, ""); err != nil || !strings.Contains(output, "LEFT linger parent_not_empty") {
		t.Fatalf("shared parent: %v: %s", err, output)
	}
	if raw, err := os.ReadFile(other); err != nil || string(raw) != "keep" {
		t.Fatal("parent cleanup changed another user's entry")
	}
	if f.disableCount() != 1 {
		t.Fatal("a shared parent prevented the normal owned entry's fixed undo")
	}
	if _, err := os.Stat(f.journal); err != nil {
		t.Fatal("partial parent cleanup discarded its retained evidence")
	}
}

func TestLingerEnableReplacementNeverBecomesOwnedUndo(t *testing.T) {
	f := newLingerTemplateFixtureV1(t, true)
	if _, err := f.run(true, "replace_enable"); err == nil {
		t.Fatal("an enable notification replacement became a confirmed original")
	}
	before, err := os.Stat(f.target)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(false, ""); err != nil || !strings.Contains(output, "LEFT linger") {
		t.Fatalf("replacement: %v: %s", err, output)
	}
	after, err := os.Stat(f.target)
	if err != nil || !os.SameFile(before, after) || f.disableCount() != 0 {
		t.Fatal("command success adopted the notification's replacement inode")
	}
}
