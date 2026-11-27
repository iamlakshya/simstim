package jail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuleChangesAreRefusedAtBothStartBoundaries(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode.name, func(t *testing.T) {
			inMode(t, mode)
			for _, boundary := range []string{"parent", "init"} {
				for _, change := range []string{"moved with link", "replaced inode"} {
					t.Run(boundary+"/"+change, func(t *testing.T) {
						root := hiddenPath(t.TempDir())
						ro, rw := filepath.Join(root, "ro"), filepath.Join(root, "rw")
						for _, dir := range []string{ro + "/bin", rw} {
							if err := os.MkdirAll(dir, 0o700); err != nil {
								t.Fatal(err)
							}
						}
						output := newOutFile(t)
						c, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", "printf SHOULD_NOT_RUN"},
							Binds: append(libBinds(), Bind{Path: ro}, Bind{Path: rw, Write: true}),
							Rules: []PathRule{{Path: ro + "/bin", Access: Exec}}, Seccomp: ProfileConverter})
						if err != nil {
							t.Fatal(err)
						}
						c.Stdout, c.Stderr = output.f, output.f
						if boundary == "init" {
							// Deliberately omit only the parent's guard. The wire set-up
							// still holds every rule and tests init independently.
							c.rules = nil
						}
						if err := os.Rename(ro+"/bin", rw+"/bin"); err != nil {
							t.Fatal(err)
						}
						if change == "moved with link" {
							err = os.Symlink(rw+"/bin", ro+"/bin")
						} else {
							err = os.Mkdir(ro+"/bin", 0o700)
						}
						if err != nil {
							t.Fatal(err)
						}
						err = c.Run()
						if boundary == "parent" {
							if err == nil || !strings.Contains(err.Error(), "Landlock rule's path") || hintOf(err) == "" {
								t.Errorf("parent accepted changed rule: %v", err)
							}
						} else {
							var ended *ExitError
							if !errors.As(err, &ended) || ended.Code != setupFailed ||
								!strings.Contains(output.String(), "Landlock rule's path") || !strings.Contains(output.String(), "changed") {
								t.Errorf("init accepted changed rule: %v, output=%q", err, output.String())
							}
						}
						if strings.Contains(output.String(), "SHOULD_NOT_RUN") {
							t.Error("argv ran after its rule moved into a writable bind")
						}
					})
				}
			}
		})
	}
}

func TestReopenRuleRejectsWrongRecordedIdentity(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, "rule"), 0o700); err != nil {
		t.Fatal(err)
	}
	checked, err := check(Spec{Argv: []string{root + "/program"}, Binds: []Bind{{Path: root}},
		Rules: []PathRule{{Path: root + "/rule", Access: Exec}}, Seccomp: ProfileConverter})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pin(checked, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	fds, err := reopen(p.binds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	config := &setup{Binds: p.binds, Rules: p.rules}
	for i := range config.Rules {
		config.Rules[i].FD = int(p.files[len(p.binds)+i].Fd())
	}
	if err := verifyRules(config, fds); err != nil {
		t.Fatalf("unchanged rule: %v", err)
	}
	config.Rules[0].Ino++
	if err := verifyRules(config, fds); err == nil || hintOf(err) == "" {
		t.Errorf("wrong recorded inode accepted: %v", err)
	}
}

func TestInitRejectsAMovedRuleLinkedBackAtItsOldName(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	root := hiddenPath(t.TempDir())
	for _, dir := range []string{"ro", "rw"} {
		if err := os.Mkdir(root+"/"+dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(root+"/ro/tool", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output := newOutFile(t)
	c, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", "printf SHOULD_NOT_RUN"},
		Binds: append(libBinds(), Bind{Path: root + "/ro"}, Bind{Path: root + "/rw", Write: true}),
		Rules: []PathRule{{Path: root + "/ro/tool", Access: Exec}}, Seccomp: ProfileConverter})
	if err != nil {
		t.Fatal(err)
	}
	c.rules = nil // independently exercise init's inherited descriptor check
	c.Stdout, c.Stderr = output.f, output.f
	if err := os.Rename(root+"/ro/tool", root+"/rw/tool"); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(root+"/rw/tool", root+"/ro/tool"); err != nil {
		t.Fatal(err)
	}
	err = c.Run()
	var ended *ExitError
	if !errors.As(err, &ended) || ended.Code != setupFailed ||
		!strings.Contains(output.String(), "pinned Landlock rule's path") || strings.Contains(output.String(), "SHOULD_NOT_RUN") {
		t.Errorf("init accepted a moved pinned object linked back at the old name: %v, %q", err, output.String())
	}
}

func TestReopenRuleDetectsReplacementDuringOpen(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"rule", "replacement"} {
		if err := os.WriteFile(root+"/"+name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checked, err := check(Spec{Argv: []string{root + "/program"}, Binds: []Bind{{Path: root}},
		Rules: []PathRule{{Path: root + "/rule", Access: Exec}}, Seccomp: ProfileConverter})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pin(checked, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	fds, err := reopen(p.binds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	config := &setup{Binds: p.binds, Rules: p.rules}
	config.Rules[0].FD = int(p.files[len(p.binds)].Fd())
	old := openat2
	defer func() { openat2 = old }()
	replaced := false
	openat2 = func(dir int, name string, how *unix.OpenHow) (int, error) {
		if dir == fds[0] && name == "rule" && !replaced {
			replaced = true
			if err := os.Rename(root+"/replacement", root+"/rule"); err != nil {
				return -1, err
			}
		}
		return old(dir, name, how)
	}
	if err := verifyRules(config, fds); err == nil || hintOf(err) == "" || !replaced {
		t.Errorf("rule replacement during open accepted: error=%v replaced=%v", err, replaced)
	}
}
