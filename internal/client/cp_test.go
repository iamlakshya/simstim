package client

import (
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/client/review"
)

type testPipeRWC struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p testPipeRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p testPipeRWC) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p testPipeRWC) Close() error {
	_ = p.w.Close()
	return p.r.Close()
}

func newTestSFTP(t *testing.T, workDir string) *sftp.Client {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv, err := sftp.NewServer(testPipeRWC{r: c2sR, w: s2cW}, sftp.WithServerWorkingDirectory(workDir))
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	go func() {
		_ = srv.Serve()
		_ = s2cW.Close()
	}()
	sc, err := sftp.NewClientPipe(s2cR, c2sW)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() {
		_ = sc.Close()
		_ = c2sW.Close()
	})
	return sc
}

func TestCPNeverImportsTar(t *testing.T) {
	fset := token.NewFileSet()
	for _, file := range []string{"cp.go", "cp_in.go", "cp_root.go"} {
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "archive/tar" {
				t.Errorf("%s imports archive/tar, which spec 21.1 forbids", file)
			}
		}
	}
}

func TestCopyFromAndToSFTPFilesAndFolders(t *testing.T) {
	remote := t.TempDir()
	local := t.TempDir()

	// Populate remote folder with a file, nested folder, and a symlink (which must be skipped).
	if err := os.MkdirAll(filepath.Join(remote, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "a.txt"), []byte("alpha\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "deep", "b.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(remote, "sub", "link")); err != nil {
		t.Fatal(err)
	}

	sc := newTestSFTP(t, remote)

	// Copy directory out.
	res, err := CopyFromSFTP(sc, "sub", local, CopyOptions{})
	if err != nil {
		t.Fatalf("CopyFromSFTP dir: %v", err)
	}
	if res.Files != 2 || len(res.Skipped) != 1 {
		t.Fatalf("res = %+v, want 2 files and 1 skipped symlink", res)
	}
	// Mode on laptop must be 0644 (guest execute bit does not cross).
	st, err := os.Stat(filepath.Join(local, "sub", "a.txt"))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("a.txt mode = %v (%v), want 0644", st, err)
	}
	// Symlink was never created on laptop.
	if _, err := os.Lstat(filepath.Join(local, "sub", "link")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlink should have been skipped, lstat=%v", err)
	}

	// Copying again without -f fails with cp_exists.
	_, err = CopyFromSFTP(sc, "sub", local, CopyOptions{})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "cp_exists" {
		t.Fatalf("second copy = %v, want cp_exists", err)
	}
	// With -f it succeeds.
	if _, err := CopyFromSFTP(sc, "sub", local, CopyOptions{Force: true}); err != nil {
		t.Fatalf("force copy = %v", err)
	}

	// Copy folder in to session.
	uploadDir := filepath.Join(local, "upload")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploadDir, "in.txt"), []byte("into-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inRes, err := CopyToSFTP(sc, uploadDir, "landed", CopyOptions{})
	if err != nil {
		t.Fatalf("CopyToSFTP: %v", err)
	}
	if inRes.Files != 1 {
		t.Fatalf("inRes = %+v", inRes)
	}
	got, err := os.ReadFile(filepath.Join(remote, "landed", "in.txt"))
	if err != nil || string(got) != "into-session\n" {
		t.Fatalf("landed/in.txt = %q, %v", got, err)
	}
	// CopyToSFTP without -f refuses overwrite of an existing file.
	if _, err := CopyToSFTP(sc, filepath.Join(uploadDir, "in.txt"), "landed/in.txt", CopyOptions{}); !errors.As(err, &ue) || ue.Code != "cp_exists" {
		t.Fatalf("CopyToSFTP overwrite = %v, want cp_exists", err)
	}
	// With -f it replaces cleanly.
	if _, err := CopyToSFTP(sc, filepath.Join(uploadDir, "in.txt"), "landed/in.txt", CopyOptions{Force: true}); err != nil {
		t.Fatalf("CopyToSFTP force overwrite = %v", err)
	}
}

func TestCPPlantedSecurityCases(t *testing.T) {
	remote := t.TempDir()
	local := t.TempDir()
	outside := t.TempDir()
	sc := newTestSFTP(t, remote)

	// 1. Flagged file (.envrc) stops with cp_flagged until --allow-flagged.
	if err := os.WriteFile(filepath.Join(remote, ".envrc"), []byte("export X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ue *UserError
	if _, err := CopyFromSFTP(sc, ".envrc", local, CopyOptions{}); !errors.As(err, &ue) || ue.Code != "cp_flagged" {
		t.Fatalf(".envrc = %v, want cp_flagged", err)
	}
	if _, err := os.Lstat(filepath.Join(local, ".envrc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".envrc was written despite refusal")
	}
	if _, err := CopyFromSFTP(sc, ".envrc", local, CopyOptions{AllowFlagged: true}); err != nil {
		t.Fatalf(".envrc with AllowFlagged: %v", err)
	}

	// 2. Hidden Unicode text in a plain file stops with cp_flagged and leaves no temp file.
	if err := os.WriteFile(filepath.Join(remote, "sneaky.txt"), []byte("hello\u202eworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFromSFTP(sc, "sneaky.txt", local, CopyOptions{}); !errors.As(err, &ue) || ue.Code != "cp_flagged" {
		t.Fatalf("sneaky.txt = %v, want cp_flagged", err)
	}
	if _, err := os.Lstat(filepath.Join(local, "sneaky.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sneaky.txt was written despite hidden text")
	}

	// 3. .git folder inside copied dir is refused with cp_git.
	if err := os.MkdirAll(filepath.Join(remote, "repo", ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "repo", ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFromSFTP(sc, "repo", local, CopyOptions{AllowFlagged: true}); !errors.As(err, &ue) || ue.Code != "cp_git" {
		t.Fatalf(".git copy = %v, want cp_git", err)
	}

	// 4. Planted laptop symlink d -> outside or d -> .git must not be followed!
	if err := os.WriteFile(filepath.Join(remote, "safe.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(local, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFromSFTP(sc, "safe.txt", filepath.Join(local, "escape", "pwn.txt"), CopyOptions{}); !errors.As(err, &ue) || ue.Code != "cp_path" {
		t.Fatalf("escape symlink = %v, want cp_path", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "pwn.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside destination through symlink!")
	}

	// Planted relative symlink inside cwd pointing to .git.
	t.Chdir(local)
	if err := os.MkdirAll(filepath.Join(local, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(local, "d")); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFromSFTP(sc, "safe.txt", "d/config", CopyOptions{}); !errors.As(err, &ue) || ue.Code != "cp_path" {
		t.Fatalf("relative symlink to .git = %v, want cp_path", err)
	}
	if _, err := os.Lstat(filepath.Join(local, ".git", "config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote into .git through relative symlink!")
	}

	// 5. Case collision (A and a) refused with cp_path.
	if err := os.MkdirAll(filepath.Join(remote, "collide"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "collide", "A"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "collide", "a"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// On a disk that ignores case (macOS by default), a overwrote A, so the
	// fake remote has only one file and nothing collides.
	if entries, err := os.ReadDir(filepath.Join(remote, "collide")); err != nil {
		t.Fatal(err)
	} else if len(entries) < 2 {
		t.Log("skipping the case collision check: this disk ignores case")
	} else if _, err := CopyFromSFTP(sc, "collide", local, CopyOptions{}); !errors.As(err, &ue) || ue.Code != "cp_path" {
		t.Fatalf("case collision = %v, want cp_path", err)
	}

	// 6. Size cap refused with cp_too_big.
	if err := os.WriteFile(filepath.Join(remote, "over.bin"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFromSFTP(sc, "over.bin", local, CopyOptions{MaxBytes: 100}); !errors.As(err, &ue) || ue.Code != "cp_too_big" {
		t.Fatalf("over cap = %v, want cp_too_big", err)
	}

	// 7. ValidateRelEntry rejects ../x and control chars.
	for _, bad := range []string{"../x", "a/../b", "bad\x00name", "bad\x1bname", ".git/config"} {
		if err := validateRelEntry(bad); err == nil {
			t.Errorf("validateRelEntry(%q) succeeded, want error", bad)
		}
	}

	// 8. CopyToSFTP refuses sensitive laptop files (.env, id_ed25519).
	envFile := filepath.Join(local, ".env")
	if err := os.WriteFile(envFile, []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyToSFTP(sc, envFile, "env.txt", CopyOptions{}); !errors.As(err, &ue) || ue.Code != "copy_sensitive" {
		t.Fatalf("CopyToSFTP(.env) = %v, want copy_sensitive", err)
	}

	// 9. Force replace replaces a destination symlink itself, never its target.
	target := filepath.Join(outside, "untouched.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkDst := filepath.Join(local, "replace-link.txt")
	if err := os.Symlink(target, linkDst); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFromSFTP(sc, "safe.txt", linkDst, CopyOptions{Force: true}); err != nil {
		t.Fatalf("force replace symlink: %v", err)
	}
	gotTarget, _ := os.ReadFile(target)
	if string(gotTarget) != "original" {
		t.Fatalf("symlink target was modified: %q", gotTarget)
	}
	gotDst, _ := os.ReadFile(linkDst)
	if string(gotDst) != "payload\n" {
		t.Fatalf("replaced dst = %q", gotDst)
	}
}

func TestCPLargeGitmodulesCRAndAbortCleanupAndSystemSymlink(t *testing.T) {
	remote := t.TempDir()
	local := t.TempDir()
	sc := newTestSFTP(t, remote)

	// 1. >1 MiB .gitmodules with CR past byte 1 MiB is hard-blocked even with AllowFlagged.
	bigGM := make([]byte, review.MaxScanSize+4096)
	for i := range bigGM {
		bigGM[i] = '#'
	}
	bigGM[review.MaxScanSize+2048] = '\r'
	if err := os.WriteFile(filepath.Join(remote, ".gitmodules"), bigGM, 0o644); err != nil {
		t.Fatal(err)
	}
	var ue *UserError
	if _, err := CopyFromSFTP(sc, ".gitmodules", local, CopyOptions{AllowFlagged: true}); !errors.As(err, &ue) || ue.Code != "cp_flagged" {
		t.Fatalf(">1 MiB .gitmodules with CR = %v, want cp_flagged", err)
	}
	if _, err := os.Lstat(filepath.Join(local, ".gitmodules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(">1 MiB .gitmodules was written despite CR")
	}

	// 2. Aborting on content review cleans up newly created destination directories,
	// and interactive confirmation runs at most once when both path and content flag.
	if err := os.MkdirAll(filepath.Join(remote, "mixed", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "mixed", ".envrc"), []byte("export A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "mixed", "nested", "hide.txt"), []byte("hi\u202e!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompts := 0
	var seenCats []string
	_, err := CopyFromSFTP(sc, "mixed", local, CopyOptions{
		ConfirmFlagged: func(res review.Result) (bool, error) {
			prompts++
			for _, f := range res.Findings {
				seenCats = append(seenCats, f.Category)
			}
			return false, nil
		},
	})
	if !errors.As(err, &ue) || ue.Code != "cp_flagged" {
		t.Fatalf("declined mixed copy = %v, want cp_flagged", err)
	}
	if prompts != 1 {
		t.Errorf("ConfirmFlagged called %d times, want 1", prompts)
	}
	if len(seenCats) < 2 {
		t.Errorf("expected both path and content findings in one prompt, got %v", seenCats)
	}
	if _, err := os.Lstat(filepath.Join(local, "mixed")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("newly created empty directory left behind after abort: %v", err)
	}

	// 3. OS top-level directory symlink (simulating macOS /tmp -> private/tmp) works
	// both as target directory itself and as parent of a target file.
	fakeRoot := t.TempDir()
	realTmp := filepath.Join(fakeRoot, "private", "tmp")
	if err := os.MkdirAll(realTmp, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeTmp := filepath.Join(fakeRoot, "tmp")
	if err := os.Symlink(realTmp, fakeTmp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "hello.txt"), []byte("mac-ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldCheck := isSystemRootSymlink
	isSystemRootSymlink = func(p string) bool { return p == fakeTmp }
	t.Cleanup(func() { isSystemRootSymlink = oldCheck })

	if _, err := CopyFromSFTP(sc, "hello.txt", fakeTmp, CopyOptions{Force: true}); err != nil {
		t.Fatalf("copy into symlinked system dir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(realTmp, "hello.txt")); err != nil || string(b) != "mac-ok\n" {
		t.Fatalf("hello.txt in realTmp = %q, %v", b, err)
	}
	// Symlink itself must still be a symlink (never replaced by -f!).
	if st, err := os.Lstat(fakeTmp); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fakeTmp symlink was altered: %v %v", st, err)
	}
	if _, err := CopyFromSFTP(sc, "hello.txt", filepath.Join(fakeTmp, "named.txt"), CopyOptions{}); err != nil {
		t.Fatalf("copy to file under symlinked system dir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(realTmp, "named.txt")); err != nil || string(b) != "mac-ok\n" {
		t.Fatalf("named.txt in realTmp = %q, %v", b, err)
	}
}
