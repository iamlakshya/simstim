package jail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamespaceCanonicalParentKeepsItsChildExecException(t *testing.T) {
	inMode(t, testMode{"namespaces", true})
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"a", "b/bin"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../b", root+"/a/link"); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"b/bin/tool": "#!/bin/sh\nprintf 'EXEC_OK\\n'\n",
		"b/other":    "#!/bin/sh\nprintf 'UNREQUESTED_EXEC\\n'\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	child := root + "/a/link/bin"
	command, err := Command(t.Context(), Spec{
		Argv: []string{"/bin/sh", "-c", `set -e
"$1"
printf value > "$2"
if "$3" 2>/dev/null; then exit 9; fi
printf 'WRITE_OK\n'`, "sh", child + "/tool", child + "/made", root + "/b/other"},
		Binds: append(libBinds(), Bind{Path: root + "/a", Write: true}, Bind{Path: child, Write: true}, Bind{Path: root + "/b", Write: true}),
		Rules: []PathRule{{Path: child, Access: Read | Write | Exec}}, Seccomp: ProfileConverter,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.CombinedOutput()
	if err != nil || string(out) != "EXEC_OK\nWRITE_OK\n" {
		t.Fatalf("canonical parent buried or widened its child exception: %v, %q", err, out)
	}
	if data, err := os.ReadFile(root + "/b/bin/made"); err != nil || string(data) != "value" {
		t.Errorf("the explicit child Write exception was lost: %v, %q", err, data)
	}
}

func TestNamespaceDirectoryLinkSuffixesStayUsable(t *testing.T) {
	for _, c := range []struct{ name, link, target string }{
		{"slash", "../run/lock/", "run/lock"},
		{"dot", "../run/lock/.", "run/lock"},
		{"parent", "../run/up/sub/..", "run/up"},
	} {
		t.Run(c.name, func(t *testing.T) {
			inMode(t, testMode{"namespaces", true})
			root := hiddenPath(t.TempDir())
			for _, name := range []string{"var", "run/lock", "run/up/sub"} {
				if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			alias := root + "/var/alias"
			if err := os.Symlink(c.link, alias); err != nil {
				t.Fatal(err)
			}
			command, err := Command(t.Context(), Spec{
				Argv:    []string{"/bin/sh", "-c", `set -e; printf value > "$1"; /bin/cat "$1"`, "sh", alias + "/made"},
				Binds:   append(libBinds(), Bind{Path: root + "/var", Write: true}, Bind{Path: alias, Write: true}, Bind{Path: root + "/run", Write: true}),
				Seccomp: ProfileConverter,
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := command.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "value" {
				t.Fatalf("ordinary directory suffix stopped working: %v, %q", err, out)
			}
			if data, err := os.ReadFile(filepath.Join(root, c.target, "made")); err != nil || string(data) != "value" {
				t.Errorf("the alias did not reach its intended writable target: %v, %q", err, data)
			}
		})
	}
}
