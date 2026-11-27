package jail

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// goodSpec returns a Spec with Linux POSIX paths. check opens no paths.
func goodSpec(t *testing.T) (Spec, string) {
	t.Helper()
	const dir = "/jail-test"
	return Spec{Argv: []string{dir + "/prog"}, Binds: []Bind{{Path: dir}}, Seccomp: ProfileGit}, dir
}

func TestCheckRefusesSpecsThatNoJailCanRun(t *testing.T) {
	s, dir := goodSpec(t)
	f, err := os.CreateTemp(t.TempDir(), "inherited-program")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cases := []struct {
		name string
		edit func(*Spec)
		want string
	}{
		{"no command", func(s *Spec) { s.Argv = nil }, "needs a command"},
		{"relative command", func(s *Spec) { s.Argv = []string{"prog"} }, "not an absolute path"},
		{"command outside the binds", func(s *Spec) { s.Argv = []string{"/usr/bin/git"} }, "outside every bound path"},
		{"a descriptor that is not a file of the jail", func(s *Spec) { s.Argv = []string{"/proc/self/fd/3"} }, "outside every bound path"},
		{"a descriptor past the files", func(s *Spec) {
			s.Argv, s.Files = []string{"/proc/self/fd/4"}, []*os.File{f}
		}, "outside every bound path"},
		{"no profile", func(s *Spec) { s.Seccomp = "" }, "unknown seccomp profile"},
		{"unknown profile", func(s *Spec) { s.Seccomp = "browser" }, "unknown seccomp profile"},
		{"unknown network", func(s *Spec) { s.Net = 7 }, "unknown network mode"},
		{"a missing file", func(s *Spec) { s.Files = []*os.File{nil} }, "inherited file is missing"},
		{"a relative bind", func(s *Spec) { s.Binds = append(s.Binds, Bind{Path: "usr"}) }, "not absolute and clean"},
		{"an unclean bind", func(s *Spec) { s.Binds = append(s.Binds, Bind{Path: "/usr/"}) }, "not absolute and clean"},
		{"the root", func(s *Spec) { s.Binds = append(s.Binds, Bind{Path: "/"}) }, "cannot bind"},
		{"proc", func(s *Spec) { s.Binds = append(s.Binds, Bind{Path: "/proc/1"}) }, "cannot bind"},
		{"twice", func(s *Spec) { s.Binds = append(s.Binds, Bind{Path: dir}) }, "bound twice"},
		{"read-only inside writable", func(s *Spec) {
			s.Binds = []Bind{{Path: dir, Write: true}, {Path: dir + "/prog"}}
		}, "with other rights"},
		{"writable inside read-only", func(s *Spec) { s.Binds = append(s.Binds, Bind{Path: dir + "/sub", Write: true}) }, "with other rights"},
		{"a rule with no right", func(s *Spec) { s.Rules = []PathRule{{Path: dir}} }, "names no known right"},
		{"a rule with an unknown right", func(s *Spec) { s.Rules = []PathRule{{Path: dir, Access: 1 << 7}} }, "names no known right"},
		{"a relative rule", func(s *Spec) { s.Rules = []PathRule{{Path: "x", Access: Read}} }, "not absolute and clean"},
		{"a rule outside the binds", func(s *Spec) { s.Rules = []PathRule{{Path: "/etc", Access: Read}} }, "outside every bound path"},
		{"a rule on /proc/self/root", func(s *Spec) { s.Rules = []PathRule{{Path: "/proc/self/root", Access: Read}} }, "outside every bound path"},
		{"a rule on /proc", func(s *Spec) { s.Rules = []PathRule{{Path: "/proc", Access: Read}} }, "outside every bound path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := s
			s.Binds = append([]Bind(nil), s.Binds...)
			c.edit(&s)
			_, err := check(s)
			var pe *proto.Error
			if !errors.As(err, &pe) {
				t.Fatalf("check = %v, want a jail error", err)
			}
			if !strings.Contains(pe.Message, c.want) || pe.Hint == "" || pe.Code != "jail" {
				t.Errorf("check = %q (hint %q, code %q), want %q and a hint", pe.Message, pe.Hint, pe.Code, c.want)
			}
		})
	}
}

func TestCheckSortsBindsAndAcceptsWhatSection20Needs(t *testing.T) {
	s, dir := goodSpec(t)
	s.Binds = []Bind{{Path: dir + "/prog"}, {Path: dir}, {Path: dir + "/a/b"}}
	s.Rules = []PathRule{{Path: dir + "/a", Access: Exec}, {Path: procSelf, Access: Read}}
	c, err := check(s)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range c.binds {
		got = append(got, strings.TrimPrefix(b.Path, dir))
	}
	if strings.Join(got, " ") != " /a/b /prog" {
		t.Errorf("binds in order %q, want parents first", got)
	}
	// A converter has no binds: it runs a program that it inherits (spec 20.2, 20.5).
	f, err := os.CreateTemp(t.TempDir(), "inherited-program")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	conv := Spec{Argv: []string{"/proc/self/fd/4", "convert"}, Files: []*os.File{f, f}, Seccomp: ProfileConverter}
	if c, err := check(conv); err != nil || c.execFD != 4 {
		t.Errorf("a converter that runs fd 4 = %+v, %v", c, err)
	}
}

// TestCheckMergesRules checks that rules on one path join, with their
// rights combined, and that the order of the rules changes nothing.
func TestCheckMergesRules(t *testing.T) {
	s, dir := goodSpec(t)
	a, b := dir+"/run", dir+"/run/tool"
	orders := [][]PathRule{
		{{Path: a, Access: Exec}, {Path: b, Access: Exec}, {Path: b, Access: Write}},
		{{Path: b, Access: Write}, {Path: a, Access: Exec}, {Path: b, Access: Exec}},
		{{Path: b, Access: Exec | Write}, {Path: a, Access: Exec}},
	}
	want := []PathRule{{Path: a, Access: Exec}, {Path: b, Access: Exec | Write}}
	for _, rules := range orders {
		s.Rules = rules
		c, err := check(s)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.rules, want) {
			t.Errorf("rules %v gave %v, want %v", rules, c.rules, want)
		}
	}
}

func TestChoose(t *testing.T) {
	sub := subID{UID: 1000, SubUID: 100000}
	have := func() (subID, bool) { return sub, true }
	none := func() (subID, bool) { return subID{}, false }
	s := Spec{Subuid: true}

	// Without a user namespace, a jail needs Landlock ABI 3.
	for _, abi := range []int{0, 1, 2} {
		_, err := choose(s, false, abi, have)
		if err == nil || !strings.Contains(err.Error(), "Landlock ABI 3 (Linux 6.2)") || hintOf(err) == "" {
			t.Errorf("ABI %d without a user namespace: %v, want a refusal with a hint", abi, err)
		}
	}
	if m, err := choose(s, false, 3, have); err != nil || m.userns || m.sub != nil {
		t.Errorf("ABI 3 without a user namespace = %+v, %v; want Landlock and seccomp alone, as the login user", m, err)
	}
	// With one, any ABI will do, and the subuid follows Spec.Subuid.
	if m, err := choose(s, true, 0, have); err != nil || !m.userns || m.sub == nil || *m.sub != sub {
		t.Errorf("a user namespace and a subuid = %+v, %v", m, err)
	}
	if m, _ := choose(s, true, 6, none); m.sub != nil {
		t.Errorf("no subuid range: sub = %+v, want the login user", m.sub)
	}
	if m, _ := choose(Spec{}, true, 6, have); m.sub != nil {
		t.Errorf("Subuid false: sub = %+v, want the login user", m.sub)
	}
	// git changes modes by path, which only namespaces let it do.
	if _, err := choose(Spec{Seccomp: ProfileGit}, false, 8, have); err == nil || !strings.Contains(err.Error(), "git jail") {
		t.Errorf("a git jail without namespaces = %v, want a refusal", err)
	}
	if _, err := choose(Spec{Seccomp: ProfileGit}, true, 8, have); err != nil {
		t.Errorf("a git jail with namespaces = %v", err)
	}
}

func TestFirstRange(t *testing.T) {
	const file = `# comment
other:100000:65536
me:bad:65536
me:200000:0
1000:300000:65536
me:400000:65536
`
	cases := []struct {
		name  string
		id    int
		start int
		ok    bool
	}{
		{"me", 1000, 300000, true}, // by number, the first good line
		{"me", 2000, 400000, true}, // by name
		{"", 1000, 300000, true},   // no name: by number only
		{"other", 7, 100000, true},
		{"nobody", 7, 0, false},
		{"", 7, 0, false},
	}
	for _, c := range cases {
		start, ok := firstRange(strings.NewReader(file), c.name, c.id)
		if start != c.start || ok != c.ok {
			t.Errorf("firstRange(%q, %d) = %d, %v; want %d, %v", c.name, c.id, start, ok, c.start, c.ok)
		}
	}
}

func TestExitError(t *testing.T) {
	for _, c := range []struct {
		e    ExitError
		want string
	}{{ExitError{Code: 3}, "code 3"}, {ExitError{Code: 125}, "on its stderr"}, {ExitError{Code: -1, Signal: 9}, "signal killed"}} {
		if got := c.e.Error(); !strings.Contains(got, c.want) {
			t.Errorf("%+v.Error() = %q, want %q", c.e, got, c.want)
		}
	}
}

// hintOf returns what to try next for a jail error.
func hintOf(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Hint
	}
	return ""
}
