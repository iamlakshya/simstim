package guest

import (
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestImageLoginShellNeverChoosesAccountCredential(t *testing.T) {
	for _, row := range []struct {
		text string
		want string
		bad  bool
	}{
		{"root:x:0:0:root:/root:/bin/false\nagent:x:1000:1000:Agent:/home/agent:/bin/sh\n", "/bin/sh", false},
		{"agent:x:0:0:root:/root:/bin/sh\n", "", true},
		{"agent:x:1000:1000:Agent:/home/agent:/bin/sh\nagent:x:1000:1000:Again:/home/agent:/bin/bash\n", "", true},
		{"agent:x:1000:1000:Agent:/home/agent:relative-shell\n", "", true},
		{"other:x:1000:1000:Other:/home/agent:/bin/sh\n", "", true},
	} {
		got, err := imageLoginShell(strings.NewReader(row.text))
		if (err != nil) != row.bad || got != row.want {
			t.Fatalf("got%q err%v want%q bad%v", got, err, row.want, row.bad)
		}
	}
}
func TestConfiguredGuestShellDoesNotBecomeShellSource(t *testing.T) {
	g := &Init{}
	a := proto.CtlStartArgs{Agent: "shell", DefaultShell: true, Shell: "/guest/bin/custom shell", Argv: []string{"old", "-l"}}
	original := append([]string(nil), a.Argv...)
	if err := g.resolveShell(&a); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Argv, []string{"/guest/bin/custom shell", "-l"}) || !reflect.DeepEqual(original, []string{"old", "-l"}) {
		t.Fatal("shell argv was interpolated or source mutated")
	}
}
func TestRootFolderAloneNeverEnablesPlainSpawner(t *testing.T) {
	restore := SetPlainHolderSpawnerForTest(nil)
	defer restore()
	g := &Init{root: "/an/arbitrary/root"}
	if g.ptyABI() != proto.GuestPTY1000V1 {
		t.Fatal("RootEnv enabled a test ABI")
	}
	// Installing the fixture cannot affect an actual production root.
	calls := 0
	reset := SetPlainHolderSpawnerForTest(func() (*syscall.SysProcAttr, error) { calls++; return &syscall.SysProcAttr{}, nil })
	defer reset()
	production := &Init{}
	_, _ = production.holderProcess()
	if calls != 0 || production.ptyABI() != proto.GuestPTY1000V1 {
		t.Fatal("test spawner escaped into a production root")
	}
}
func TestCustomCommandDoesNotReadImageShell(t *testing.T) {
	a := proto.CtlStartArgs{Agent: "shell", Argv: []string{"custom", "-c", "literal"}}
	if err := (&Init{root: t.TempDir()}).resolveShell(&a); err != nil {
		t.Fatal(err)
	}
	if a.Argv[0] != "custom" {
		t.Fatal("a custom command was replaced")
	}
}
