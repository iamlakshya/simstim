package guest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elastic/go-seccomp-bpf/arch"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/net/bpf"
)

func TestPurgeReaderSecurityTransitionStaysOnFixedExecThreadV1(t *testing.T) {
	tree, err := parser.ParseFile(token.NewFileSet(), "purge_reader_helper_linux.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, declaration := range tree.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "RunPurgeReaderGitHelperV1" {
			body = function.Body
		}
	}
	if body == nil {
		t.Fatal("the closed reader helper is missing")
	}
	deferred := map[*ast.CallExpr]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		if call, ok := node.(*ast.DeferStmt); ok {
			deferred[call.Call] = true
		}
		if _, ok := node.(*ast.GoStmt); ok {
			t.Error("the fixed credential/filter/exec sequence cannot leave its bound goroutine")
		}
		return true
	})
	var lock, drop, filter, execute token.Pos
	locks, unlocks := 0, 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch function := call.Fun.(type) {
		case *ast.Ident:
			if function.Name == "dropFileCommitAccount" {
				drop = call.Pos()
			}
			if function.Name == "installPurgeReaderFilterV1" {
				filter = call.Pos()
			}
		case *ast.SelectorExpr:
			if name, ok := function.X.(*ast.Ident); ok {
				switch name.Name + "." + function.Sel.Name {
				case "runtime.LockOSThread":
					lock, locks = call.Pos(), locks+1
				case "runtime.UnlockOSThread":
					unlocks++
					if !deferred[call] {
						t.Error("the fixed exec thread was released before the helper returned")
					}
				case "unix.Exec":
					execute = call.Pos()
				}
			}
		}
		return true
	})
	if locks != 1 || unlocks != 1 || lock == token.NoPos || !(lock < drop && drop < filter && filter < execute) {
		t.Fatal("the native-thread lock no longer covers credential/security transition and fixed exec")
	}
}

type purgeReaderDirectoryTestV1 struct{}

func (purgeReaderDirectoryTestV1) request(proto.Request, func(any, error)) {}
func (purgeReaderDirectoryTestV1) probe(context.Context, string, ...string) (string, int, error) {
	return "", 125, nil
}
func (purgeReaderDirectoryTestV1) wait(*execRun) (bool, error) { return false, nil }

func TestPurgeReaderDirectoryMetadataIsBoundedAndCancelledV1(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 140; index++ {
		name := filepath.Join(root, strings.Repeat("a", index+1))
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	g := &Init{purgeReader: purgeReaderDirectoryTestV1{}}
	for _, limit := range []int{1, 129} {
		entries, err := g.purgeWorkDirectoryV1(t.Context(), root, limit)
		if err != nil || len(entries) != limit {
			t.Fatal("reader directory metadata exceeded or lost its bound", limit, len(entries), err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if entries, err := g.purgeWorkDirectoryV1(ctx, filepath.Join(root, "missing"), 129); err != context.Canceled || entries != nil {
		t.Fatal("cancelled reader metadata reached another filesystem operation", len(entries), err)
	}
}

func TestPurgeReaderAdmitsOnlyExactProtectedInspectionV1(t *testing.T) {
	plan := proto.PurgeWorkInspectArgsV1{Version: 1, Primary: "primary2", Repositories: []proto.PurgeWorkRepositoryV1{{Name: "repo", Saved: []string{strings.Repeat("a", 40)}}}}
	raw, err := json.Marshal(plan)
	if err != nil || purgeReaderRequestV1(proto.CtlPurgeWorkInspectV1, raw) != nil {
		t.Fatal("the exact protected work plan was refused", err)
	}
	for _, operation := range []string{proto.CtlVMPrepare, proto.CtlPrepare, proto.CtlSpawn, proto.CtlExec, proto.CtlBundlePrepare, proto.CtlGuestCapabilities, "purge.reader-git.v1", ""} {
		if purgeReaderRequestV1(operation, raw) == nil {
			t.Fatal("another operation reached reader preparation", operation)
		}
	}
	if purgeReaderRequestV1(proto.CtlPurgeWorkInspectV1, json.RawMessage(`{"v":1,"primary":"primary2","repositories":[{"name":"repo","saved":[],"argv":["/bin/sh"]}]}`)) == nil {
		t.Fatal("wire argv became a protected reader plan")
	}
}

func TestPurgeReaderGitGrammarCannotBecomeAnExecutionAPI(t *testing.T) {
	oid := strings.Repeat("a", 40)
	valid := [][]string{{"--version"}, {"rev-parse", "--show-toplevel"}, {"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none"}, {"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}, {"symbolic-ref", "--quiet", "HEAD"}, {"show-ref", "--verify", "--quiet", "refs/heads/main"}, {"cat-file", "-e", oid + "^{commit}"}, {"rev-list", "--count", "--all", oid, "--not", oid}, {"rev-list", "--count", "--all", "--not"}}
	for _, args := range valid {
		if err := validatePurgeReaderGitV1(purgeReaderGitRequestV1{Directory: "/work/repo", Arguments: args}); err != nil {
			t.Fatal("a fixed source probe was refused", args, err)
		}
	}
	for _, request := range []purgeReaderGitRequestV1{
		{Directory: "/work/../etc", Arguments: valid[0]},
		{Directory: "/work/repo/.git", Arguments: valid[0]},
		{Directory: "/work/repo", Arguments: []string{"config", "core.hooksPath", "/work/repo/hooks"}},
		{Directory: "/work/repo", Arguments: []string{"-c", "alias.status=!sh", "status"}},
		{Directory: "/work/repo", Arguments: []string{"show-ref", "--verify", "--quiet", "refs/heads/../../other"}},
		{Directory: "/work/repo", Arguments: []string{"cat-file", "-e", "--help"}},
		{Directory: "/work/repo", Arguments: []string{"rev-list", "--count", "--all", "--not", "--objects"}},
	} {
		if validatePurgeReaderGitV1(request) == nil {
			t.Fatal("the fixed reader accepted another command or path", request)
		}
	}
}

func TestPurgeReaderFilterRefusesNetworkChildrenAndForeignABI(t *testing.T) {
	for _, target := range []string{"amd64", "arm64"} {
		raw, err := purgeReaderFilterV1(target)
		if err != nil {
			t.Fatal(err)
		}
		instructions, ok := bpf.Disassemble(raw)
		if !ok {
			t.Fatal("the reader filter does not disassemble")
		}
		machine, err := bpf.NewVM(instructions)
		if err != nil {
			t.Fatal(err)
		}
		info, err := arch.GetInfo(target)
		if err != nil {
			t.Fatal(err)
		}
		verdict := func(number, native uint32) uint32 {
			packet := make([]byte, 64)
			binary.BigEndian.PutUint32(packet, number) // bpf VM word order, not a native syscall.
			binary.BigEndian.PutUint32(packet[4:], native)
			value, err := machine.Run(packet)
			if err != nil {
				t.Fatal(err)
			}
			return uint32(value)
		}
		for _, name := range purgeReaderDeniedV1 {
			if number, exists := info.SyscallNames[name]; exists && verdict(uint32(number), uint32(info.ID)) != 0x00050001 {
				t.Fatal("the reader permits a refused native call", target, name)
			}
		}
		for _, name := range []string{"read", "write", "execve", "exit_group", "mmap"} {
			if verdict(uint32(info.SyscallNames[name]), uint32(info.ID)) != 0x7fff0000 {
				t.Fatal("a fixed Git process cannot use its ordinary IO", target, name)
			}
		}
		if verdict(0, 0) != 0x80000000 || verdict(0x40000000, uint32(info.ID)) != 0x80000000 {
			t.Fatal("a foreign or x32 ABI bypasses the fixed reader filter", target)
		}
	}
}
