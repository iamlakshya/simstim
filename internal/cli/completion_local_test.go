package cli

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestCompletionKeepsLongLocalSecretNames(t *testing.T) {
	cfg, _ := isolateCompletionEnv(t)
	names := []string{strings.Repeat("A", 65), strings.Repeat("B", 513)}
	text := "[secrets]\n"
	for _, name := range names {
		text += name + " = 'printf never-run'\n"
	}
	text += "\"bad\\tkey\" = 'printf never-run'\n"
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "A", "B"} {
		got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "new", "box", "--secret", prefix)
		want := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return !strings.HasPrefix(name, prefix) })
		if !slices.Equal(got, want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("prefix %q: got %q, directive %d, stderr %q, want %q", prefix, got, directive, stderr, want)
		}
	}
}

func TestCompletionOpenedFileKeepsTheByteLimit(t *testing.T) {
	for _, size := range []int{maxCompletionBytes - 1, maxCompletionBytes, maxCompletionBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			data := bytes.Repeat([]byte("x"), size)
			path := filepath.Join(t.TempDir(), "cache.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := readCompletionFile(path)
			if ok != (size <= maxCompletionBytes) {
				t.Fatalf("%d bytes: accepted %v", size, ok)
			}
			if ok && !bytes.Equal(got, data) {
				t.Fatal("a valid local file was changed or truncated")
			}
			if !ok && got != nil {
				t.Fatal("an oversized local file returned partial bytes")
			}
		})
	}
}

func TestCompletionRefusesAnOpenedPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	done := make(chan bool, 1)
	go func() { _, ok := readCompletionData(reader); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("completion accepted an opened pipe")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion waited for an opened pipe's writer")
	}
}

// The opened-descriptor checks need a nonblocking open at the real entry.
// This checks that production discipline without adding a race-only hook.
func TestCompletionOpensWithoutWaitingAndChecksTheSameFile(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "completion_read.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok {
			funcs[f.Name.Name] = f
		}
	}
	open, read := funcs["readCompletionFile"], funcs["readCompletionData"]
	if open == nil || read == nil {
		t.Fatal("completion has no descriptor-bound local reader")
	}
	opens, stats, reads, closes := 0, 0, 0, 0
	ast.Inspect(open, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if receiver.Name == "os" {
			if sel.Sel.Name != "OpenFile" || len(call.Args) != 3 {
				t.Errorf("completion uses pathname operation os.%s", sel.Sel.Name)
				return true
			}
			flags, ok := call.Args[1].(*ast.BinaryExpr)
			if !ok || flags.Op != token.OR || !completionSelector(flags.X, "os", "O_RDONLY") || !completionSelector(flags.Y, "syscall", "O_NONBLOCK") {
				t.Error("completion can wait for a replaced FIFO")
			}
			opens++
		}
		if receiver.Name == "f" && sel.Sel.Name == "Close" {
			closes++
		}
		return true
	})
	ast.Inspect(open, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "readCompletionData" && len(call.Args) == 1 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "f" {
					reads++
				}
			}
		}
		return true
	})
	ast.Inspect(read, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if completionSelector(call.Fun, "f", "Stat") {
			stats++
		}
		if completionSelector(call.Fun, "os", "Stat") || completionSelector(call.Fun, "os", "Open") || completionSelector(call.Fun, "os", "OpenFile") {
			t.Error("completion reopens or checks a pathname instead of its descriptor")
		}
		return true
	})
	if opens != 1 || closes != 1 || reads != 1 || stats != 1 {
		t.Fatalf("descriptor ownership: opens=%d closes=%d reads=%d stats=%d", opens, closes, reads, stats)
	}
}

func completionSelector(expr ast.Expr, receiver, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == receiver
}
