package e2b

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// The package signals no process by a bare number: by the time a signal
// goes, the number may belong to another process. Signal 0 is no exception:
// it would ask about that other process. The check below parses every Go
// file of the package, for every system, and reports these forms anywhere
// in a file, also in package-level declarations:
//
//   - a use of Kill, Killpg, Tgkill, Tkill or PidfdSendSignal, or of a
//     SYS_ number for one of them, from syscall or golang.org/x/sys/unix,
//     under any import name, also as a value. Two calls remain, both in the
//     wrapper's endGroup: Kill(0, sig) and Kill(-group, sig), which hit the
//     wrapper's own group while the wrapper lives;
//   - os.FindProcess, and an os.Process made from a literal;
//   - the Kill or Signal method of x.Process, or of an os.Process literal,
//     used as a value, not called;
//   - the word kill, pkill or killall in a string given to exec.Command,
//     exec.CommandContext, or to a Run or Start call as its second argument,
//     the command line of the fake's Envd.
//
// An owned child is signalled through the os.Process of its exec.Cmd, which
// never signals a child that its Wait has reaped. The check sees only the Go
// source: a kill that a program builds at run time can pass it.
func TestNoKillByABareProcessNumber(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range badKills(fset, f) {
			t.Error(bad)
		}
	}
}

// The check reports every form that a review found to pass its first
// version, and the two calls of endGroup pass it.
func TestSignalPolicyFindsEveryForm(t *testing.T) {
	const src = `package p

import (
	"os"
	"os/exec"
	sys "syscall"
	"syscall"
	. "golang.org/x/sys/unix"
)

var killVar = func(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

func alias(pid int)              { _ = sys.Kill(pid, sys.SIGKILL) }
func literal(pid int)            { _ = (&os.Process{Pid: pid}).Kill() }
func value(pid int)              { kill := syscall.Kill; _ = kill(pid, 9) }
func methodValue(c *exec.Cmd)    { f := c.Process.Kill; _ = f() }
func shell(pid string)           { _ = exec.Command("sh", "-c", "kill -9 "+pid).Run() }
func pkill()                     { _ = exec.Command("pkill", "sleep").Run() }
func raw(pid int)                { _, _, _ = syscall.Syscall(syscall.SYS_KILL, uintptr(pid), 9, 0) }
func dot(pid int)                { _ = Kill(pid, 9) }
func find(pid int)               { _, _ = os.FindProcess(pid) }
func script(e interface{ Run(any, string, any) }) { e.Run(nil, "sleep 9 & kill $!", nil) }
func endGroup(group int) {
	_ = syscall.Kill(-group, syscall.SIGKILL)
	_ = syscall.Kill(0, syscall.SIGKILL)
	_ = syscall.Kill(group, syscall.SIGKILL)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	lines := map[int]bool{}
	for _, bad := range badKills(fset, f) {
		lines[bad.line] = true
	}
	for _, want := range []int{11, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 26} {
		if !lines[want] {
			t.Errorf("line %d: not reported", want)
		}
		delete(lines, want)
	}
	for line := range lines {
		t.Errorf("line %d: reported, but it is allowed", line)
	}
}

type badKill struct {
	pos  token.Position
	line int
	why  string
}

func (b badKill) String() string { return b.pos.String() + ": " + b.why }

var (
	signalFuncs = map[string]bool{
		"Kill": true, "Killpg": true, "Tgkill": true, "Tkill": true, "PidfdSendSignal": true,
		"SYS_KILL": true, "SYS_TKILL": true, "SYS_TGKILL": true, "SYS_PIDFD_SEND_SIGNAL": true,
	}
	killWord = regexp.MustCompile(`\b(kill|pkill|killall)\b`)
)

// badKills lists the uses in f that may signal a process by its number.
func badKills(fset *token.FileSet, f *ast.File) []badKill {
	// The names under which f imports each package that can signal.
	sys, osName, execName := map[string]bool{}, "", ""
	dotSys := false
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch path {
		case "syscall", "golang.org/x/sys/unix":
			if name == "." {
				dotSys = true
			}
			sys[name] = true
		case "os":
			osName = name
		case "os/exec":
			execName = name
		}
	}
	pkgSel := func(e ast.Expr, pkg string) (string, bool) {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		id, ok := s.X.(*ast.Ident)
		if !ok || id.Name != pkg || pkg == "" {
			return "", false
		}
		return s.Sel.Name, true
	}
	// The calls that endGroup may make, every expression that a call calls,
	// and every name after a dot.
	allowed, called, after := map[ast.Node]bool{}, map[ast.Node]bool{}, map[*ast.Ident]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "endGroup" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && len(c.Args) == 2 && (isZero(c.Args[0]) || isMinus(c.Args[0], "group")) {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Kill" {
					if id, ok := s.X.(*ast.Ident); ok && sys[id.Name] {
						allowed[s] = true
					}
				}
			}
			return true
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			called[n.Fun] = true
		case *ast.SelectorExpr:
			after[n.Sel] = true
		}
		return true
	})
	var bad []badKill
	report := func(n ast.Node, why string) {
		pos := fset.Position(n.Pos())
		bad = append(bad, badKill{pos: pos, line: pos.Line, why: why})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && sys[id.Name] && signalFuncs[n.Sel.Name] {
				if !allowed[n] {
					report(n, id.Name+"."+n.Sel.Name+" signals a process by its number")
				}
				return true
			}
			if name, ok := pkgSel(n, osName); ok && name == "FindProcess" {
				report(n, "os.FindProcess finds a process by its number")
				return true
			}
			if (n.Sel.Name == "Kill" || n.Sel.Name == "Signal") && !called[n] && isProcess(n.X, osName) {
				report(n, "a "+n.Sel.Name+" method as a value can signal a process that no one checks")
			}
		case *ast.Ident:
			if dotSys && signalFuncs[n.Name] && !after[n] && n.Obj == nil {
				report(n, n.Name+", imported with a dot, signals a process by its number")
			}
		case *ast.CompositeLit:
			if name, ok := pkgSel(n.Type, osName); ok && name == "Process" {
				report(n, "an os.Process made from a number signals that number")
			}
		case *ast.CallExpr:
			var args []ast.Expr
			if name, ok := pkgSel(n.Fun, execName); ok && (name == "Command" || name == "CommandContext") {
				args = n.Args
			} else if s, ok := n.Fun.(*ast.SelectorExpr); ok && (s.Sel.Name == "Run" || s.Sel.Name == "Start") && len(n.Args) >= 2 {
				args = n.Args[1:2]
			}
			for _, a := range args {
				ast.Inspect(a, func(m ast.Node) bool {
					if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil && killWord.MatchString(s) {
							report(lit, "a command line that runs kill, pkill or killall")
						}
					}
					return true
				})
			}
		}
		return true
	})
	sort.Slice(bad, func(i, j int) bool { return bad[i].pos.Offset < bad[j].pos.Offset })
	return bad
}

// isProcess says that e is x.Process, or an os.Process literal, in
// parentheses or behind an & or not.
func isProcess(e ast.Expr, osName string) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			e = x.X
		case *ast.SelectorExpr:
			return x.Sel.Name == "Process"
		case *ast.CompositeLit:
			s, ok := x.Type.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			id, ok := s.X.(*ast.Ident)
			return ok && id.Name == osName && s.Sel.Name == "Process"
		default:
			return false
		}
	}
}

func isZero(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Value == "0"
}

func isMinus(e ast.Expr, name string) bool {
	u, ok := e.(*ast.UnaryExpr)
	if !ok || u.Op != token.SUB {
		return false
	}
	id, ok := u.X.(*ast.Ident)
	return ok && id.Name == name
}
