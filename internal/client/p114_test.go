package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestResolveEnvPrecedenceAndSecretRefusal(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "dev.env")
	content := strings.Join([]string{
		"# comment line",
		"",
		"FROM_FILE=file-val",
		`QUOTED="keep quotes"`,
		"NO_EXPAND=$HOME",
		"FROM_LAPTOP",
		"SHARED=from-file",
	}, "\n")
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s := DefaultSettings()
	s.Sandbox.Env = map[string]string{
		"LAYER_ONLY": "global",
		"SHARED":     "global",
	}
	s.Secrets = map[string]any{
		"API_SECRET": "echo secret",
	}
	h := Host{
		Name: "box",
		Sandbox: &HostSandbox{
			Env: map[string]string{
				"SHARED":    "host",
				"HOST_ONLY": "hval",
			},
		},
	}
	laptop := map[string]string{
		"FROM_LAPTOP": "laptop-val",
		"FLAG_LOOKUP": "from-shell",
	}
	lookup := func(k string) (string, bool) {
		v, ok := laptop[k]
		return v, ok
	}

	got, err := ResolveEnv(s, h, []string{envFile}, []string{"SHARED=first-flag", "SHARED=last-flag", "FLAG_LOOKUP"}, lookup)
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	want := map[string]string{
		"LAYER_ONLY":  "global",
		"HOST_ONLY":   "hval",
		"FROM_FILE":   "file-val",
		"QUOTED":      `"keep quotes"`,
		"NO_EXPAND":   "$HOME",
		"FROM_LAPTOP": "laptop-val",
		"SHARED":      "last-flag",
		"FLAG_LOOKUP": "from-shell",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("env[%q] = %q, want %q", k, got[k], w)
		}
	}

	// Missing laptop variable fails with env_missing (KindUsage -> exit 2).
	_, err = ResolveEnv(s, h, nil, []string{"NOT_SET_ON_LAPTOP"}, lookup)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "env_missing" || ue.Kind != KindUsage {
		t.Fatalf("missing var err = %v, want env_missing/KindUsage", err)
	}

	// Secret name refused with env_is_secret (KindRefused -> exit 5).
	_, err = ResolveEnv(s, h, nil, []string{"API_SECRET=oops"}, lookup)
	if !errors.As(err, &ue) || ue.Code != "env_is_secret" || ue.Kind != KindRefused {
		t.Fatalf("secret env err = %v, want env_is_secret/KindRefused", err)
	}
}

func TestParseAndValidateCopyIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "review.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	fixture := filepath.Join(outsideDir, "fixture.json")
	if err := os.WriteFile(fixture, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := ParseCopyInSpecs([]string{
		"~/.claude/skills",
		fixture,
		fixture + ":~/custom/fixture.json",
	}, home)
	if err != nil {
		t.Fatalf("ParseCopyInSpecs: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	if items[0].GuestPath != "/home/agent/.claude/skills" {
		t.Errorf("home default guest path = %q, want /home/agent/.claude/skills", items[0].GuestPath)
	}
	if items[1].GuestPath != "/mnt/fixture.json" {
		t.Errorf("outside default guest path = %q, want /mnt/fixture.json", items[1].GuestPath)
	}
	if items[2].GuestPath != "/home/agent/custom/fixture.json" {
		t.Errorf("~/ guest path = %q, want /home/agent/custom/fixture.json", items[2].GuestPath)
	}
	if err := ValidateCopyIn(items, DefaultCopyInMaxBytes, DefaultCopyInMaxEntries); err != nil {
		t.Fatalf("ValidateCopyIn: %v", err)
	}

	// Two different laptop files targeting the same guest path -> copy_conflict (KindUsage).
	other := filepath.Join(outsideDir, "other.json")
	_ = os.WriteFile(other, []byte("{}"), 0o600)
	_, err = ParseCopyInSpecs([]string{fixture + ":/mnt/same.json", other + ":/mnt/same.json"}, home)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "copy_conflict" || ue.Kind != KindUsage {
		t.Fatalf("collision err = %v, want copy_conflict/KindUsage", err)
	}

	// Relative guest path -> bad_value (KindUsage).
	_, err = ParseCopyInSpecs([]string{fixture + ":relative/path"}, home)
	if !errors.As(err, &ue) || ue.Code != "bad_value" || ue.Kind != KindUsage {
		t.Fatalf("relative guest path err = %v, want bad_value/KindUsage", err)
	}

	// Sensitive laptop file (~/.ssh/id_ed25519) -> copy_sensitive (KindRefused).
	sshDir := filepath.Join(home, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)
	keyFile := filepath.Join(sshDir, "id_ed25519")
	_ = os.WriteFile(keyFile, []byte("PRIVATE"), 0o600)
	sensItems, err := ParseCopyInSpecs([]string{"~/.ssh/id_ed25519"}, home)
	if err != nil {
		t.Fatalf("ParseCopyInSpecs sensitive: %v", err)
	}
	err = ValidateCopyIn(sensItems, DefaultCopyInMaxBytes, DefaultCopyInMaxEntries)
	if !errors.As(err, &ue) || ue.Code != "copy_sensitive" || ue.Kind != KindRefused {
		t.Fatalf("sensitive file err = %v, want copy_sensitive/KindRefused", err)
	}

	// Aggregate size cap enforcement -> cp_too_big (KindRefused).
	err = ValidateCopyIn(items, 4, DefaultCopyInMaxEntries)
	if !errors.As(err, &ue) || ue.Code != "cp_too_big" || ue.Kind != KindRefused {
		t.Fatalf("cap err = %v, want cp_too_big/KindRefused", err)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPrepareSeedsCleanAndDirtyNoCleanFilter(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	repo := filepath.Join(parent, "myrepo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init", "-b", "feature")
	runTestGit(t, repo, "config", "user.name", "Laptop Dev")
	runTestGit(t, repo, "config", "user.email", "dev@example.com")
	runTestGit(t, repo, "remote", "add", "origin", "https://example.invalid/org/myrepo.git")
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "gone.txt"), []byte("bye\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Configure a clean filter with a sentinel file to prove --seed-dirty never runs it.
	sentinel := filepath.Join(parent, "clean-filter-ran")
	runTestGit(t, repo, "config", "filter.evil.clean", "touch '"+sentinel+"'; cat")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", ".")
	// Clear sentinel created by setup's own git add.
	_ = os.Remove(sentinel)
	runTestGit(t, repo, "commit", "-m", "initial")

	// Dirty changes: modify keep.txt, delete gone.txt, add untracked + symlink.
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("v2-dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("hello-untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("keep.txt", filepath.Join(repo, "link.txt")); err != nil {
		t.Fatal(err)
	}

	statusBefore := runTestGit(t, repo, "status", "--porcelain")
	refsBefore := runTestGit(t, repo, "show-ref")
	// Remove sentinel after the test's own git status snapshot so we strictly
	// measure whether PrepareSeeds triggers the repository clean filter.
	_ = os.Remove(sentinel)

	var stderr bytes.Buffer
	seeds, err := PrepareSeeds(ctx, []string{repo}, "", true, DefaultSeedMaxBytes, &stderr)
	if err != nil {
		t.Fatalf("PrepareSeeds dirty: %v", err)
	}
	defer seeds.Cleanup()

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("repository clean filter executed during --seed-dirty")
	}
	if statusAfter := runTestGit(t, repo, "status", "--porcelain"); statusAfter != statusBefore {
		t.Fatalf("git status changed:\nbefore:\n%s\nafter:\n%s", statusBefore, statusAfter)
	}
	if refsAfter := runTestGit(t, repo, "show-ref"); refsAfter != refsBefore {
		t.Fatalf("git refs changed:\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
	}
	if !strings.Contains(stderr.String(), "untracked.txt") {
		t.Errorf("stderr did not list untracked.txt:\n%s", stderr.String())
	}

	// Clone the produced bundle and verify the dirty commit's contents.
	cloneDir := filepath.Join(t.TempDir(), "cloned")
	runTestGit(t, filepath.Dir(cloneDir), "clone", "--branch", "feature", seeds.Items[0].BundlePath, cloneDir)
	if got, err := os.ReadFile(filepath.Join(cloneDir, "keep.txt")); err != nil || string(got) != "v2-dirty\n" {
		t.Errorf("cloned keep.txt = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(cloneDir, "untracked.txt")); err != nil || string(got) != "hello-untracked\n" {
		t.Errorf("cloned untracked.txt = %q, err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(cloneDir, "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("deleted tracked file gone.txt appeared in dirty clone")
	}
	if target, err := os.Readlink(filepath.Join(cloneDir, "link.txt")); err != nil || target != "keep.txt" {
		t.Errorf("cloned symlink = %q, err=%v", target, err)
	}
	if seeds.Items[0].Origin != "https://example.invalid/org/myrepo.git" || seeds.Items[0].UserName != "Laptop Dev" || seeds.Items[0].UserEmail != "dev@example.com" {
		t.Errorf("seed metadata = %+v", seeds.Items[0])
	}

	// Untracked private file (.env) refused with copy_sensitive.
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = PrepareSeeds(ctx, []string{repo}, "", true, DefaultSeedMaxBytes, &stderr)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "copy_sensitive" || ue.Kind != KindRefused {
		t.Fatalf("sensitive untracked err = %v, want copy_sensitive/KindRefused", err)
	}
	_ = os.Remove(filepath.Join(repo, ".env"))

	// Duplicate base name refused with seed_conflict.
	otherParent := t.TempDir()
	dupRepo := filepath.Join(otherParent, "myrepo")
	_ = os.MkdirAll(dupRepo, 0o700)
	runTestGit(t, dupRepo, "init", "-b", "main")
	_ = os.WriteFile(filepath.Join(dupRepo, "a"), []byte("a"), 0o600)
	runTestGit(t, dupRepo, "add", ".")
	runTestGit(t, dupRepo, "commit", "-m", "init")
	_, err = PrepareSeeds(ctx, []string{repo, dupRepo}, "", false, DefaultSeedMaxBytes, &stderr)
	if !errors.As(err, &ue) || ue.Code != "seed_conflict" || ue.Kind != KindUsage {
		t.Fatalf("duplicate seed name err = %v, want seed_conflict/KindUsage", err)
	}
}

func newLocalSFTPTestClient(t *testing.T, rootDir string) *sftp.Client {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{Reader: c2sR, WriteCloser: s2cW}, sftp.WithServerWorkingDirectory(rootDir))
	if err != nil {
		t.Fatalf("sftp.NewServer: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve()
		_ = s2cW.Close()
		_ = c2sR.Close()
	}()
	sc, err := sftp.NewClientPipe(s2cR, c2sW)
	if err != nil {
		_ = c2sW.Close()
		_ = s2cR.Close()
		<-done
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}
	t.Cleanup(func() {
		_ = sc.Close()
		_ = c2sW.Close()
		_ = s2cR.Close()
		<-done
	})
	return sc
}

func TestCopyInMultiItemCapsValidateAndUpload(t *testing.T) {
	srcDir := t.TempDir()
	f10 := filepath.Join(srcDir, "ten.txt")
	f0 := filepath.Join(srcDir, "zero.txt")
	f1 := filepath.Join(srcDir, "one.txt")
	if err := os.WriteFile(f10, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f0, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f1, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Exact byte boundary (10 bytes) + 0-byte second file fits in maxBytes=10.
	exactItems := []CopyInItem{
		{Source: f10, LocalPath: f10, GuestPath: "/mnt/ten.txt"},
		{Source: f0, LocalPath: f0, GuestPath: "/mnt/zero.txt"},
	}
	if err := ValidateCopyIn(exactItems, 10, 10); err != nil {
		t.Fatalf("ValidateCopyIn exact boundary + zero file: %v", err)
	}
	stage1 := t.TempDir()
	sc1 := newLocalSFTPTestClient(t, stage1)
	uploaded, err := UploadCopyIn(sc1, exactItems, 10, 10)
	if err != nil || len(uploaded) != 2 {
		t.Fatalf("UploadCopyIn exact boundary + zero file = %v, %v", uploaded, err)
	}

	// Exact byte boundary (10 bytes) + 1-byte second file fails with configured cap (10 B, not 0 B)
	// and must not upload the second item.
	overByteItems := []CopyInItem{
		{Source: f10, LocalPath: f10, GuestPath: "/mnt/ten.txt"},
		{Source: f1, LocalPath: f1, GuestPath: "/mnt/one.txt"},
	}
	var ue *UserError
	err = ValidateCopyIn(overByteItems, 10, 10)
	if !errors.As(err, &ue) || ue.Code != "cp_too_big" || strings.Contains(ue.Msg, " 0 bytes") || !strings.Contains(ue.Msg, " 10 bytes") {
		t.Fatalf("ValidateCopyIn over-byte err = %v, want cp_too_big naming 10 bytes", err)
	}
	stage2 := t.TempDir()
	sc2 := newLocalSFTPTestClient(t, stage2)
	_, err = UploadCopyIn(sc2, overByteItems, 10, 10)
	if !errors.As(err, &ue) || ue.Code != "cp_too_big" || strings.Contains(ue.Msg, " 0 bytes") || !strings.Contains(ue.Msg, " 10 bytes") {
		t.Fatalf("UploadCopyIn over-byte err = %v, want cp_too_big naming 10 bytes", err)
	}
	if _, statErr := os.Stat(filepath.Join(stage2, "copy", "1", "one.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("UploadCopyIn uploaded over-cap second item: statErr=%v", statErr)
	}

	// Entry cap counts both directories and files across items:
	// dirWithSub has 2 entries (sub/ + sub/a.txt); adding f0 exceeds maxEntries=2.
	dirWithSub := filepath.Join(srcDir, "tree")
	if err := os.MkdirAll(filepath.Join(dirWithSub, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirWithSub, "sub", "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	overEntryItems := []CopyInItem{
		{Source: dirWithSub, LocalPath: dirWithSub, GuestPath: "/mnt/tree"},
		{Source: f0, LocalPath: f0, GuestPath: "/mnt/zero.txt"},
	}
	err = ValidateCopyIn(overEntryItems, 1024, 2)
	if !errors.As(err, &ue) || ue.Code != "cp_too_big" || !strings.Contains(ue.Msg, "2") || strings.Contains(ue.Msg, " 0 ") {
		t.Fatalf("ValidateCopyIn over-entry err = %v, want cp_too_big naming 2", err)
	}
	stage3 := t.TempDir()
	sc3 := newLocalSFTPTestClient(t, stage3)
	_, err = UploadCopyIn(sc3, overEntryItems, 1024, 2)
	if !errors.As(err, &ue) || ue.Code != "cp_too_big" || !strings.Contains(ue.Msg, "2") {
		t.Fatalf("UploadCopyIn over-entry err = %v, want cp_too_big naming 2", err)
	}
	if _, statErr := os.Stat(filepath.Join(stage3, "copy", "1", "zero.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("UploadCopyIn uploaded over-entry item: statErr=%v", statErr)
	}
}

func TestResolveSandboxSizesAutoAndNegativeHostCPUs(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ConfigDir(), "config.toml"), []byte("[sessions]\nmemory_max = \"2G\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Negative host.sandbox.cpus returns bad_config even when global sandbox.cpus is positive.
	s := DefaultSettings()
	s.Sandbox.CPUs = 2
	hNeg := Host{Name: "box", Sandbox: &HostSandbox{CPUs: -1}}
	var ue *UserError
	if _, err := ResolveSandboxSizes(s, hNeg); !errors.As(err, &ue) || ue.Code != "bad_config" {
		t.Fatalf("negative host.sandbox.cpus err = %v, want bad_config", err)
	}

	// [sandbox] memory = "auto" returns MemoryAuto so withMemoryMax does not fall back to [sessions] memory_max.
	sAuto := DefaultSettings()
	sAuto.Sandbox.Memory = "auto"
	sizes, err := ResolveSandboxSizes(sAuto, Host{Name: "box"})
	if err != nil {
		t.Fatalf("ResolveSandboxSizes global auto: %v", err)
	}
	if sizes.Memory != MemoryAuto {
		t.Fatalf("global sandbox.memory=auto -> %d, want MemoryAuto (%d)", sizes.Memory, MemoryAuto)
	}
	resolvedArgs, err := withMemoryMax(NewArgs{MemoryMax: sizes.Memory})
	if err != nil || resolvedArgs.MemoryMax != 0 {
		t.Fatalf("withMemoryMax(MemoryAuto) = %+v, %v; want MemoryMax=0 (host default), not [sessions] memory_max", resolvedArgs, err)
	}

	// [host.sandbox] memory = "auto" overrides [sandbox] memory = "4G" and also yields MemoryAuto.
	s4G := DefaultSettings()
	s4G.Sandbox.Memory = "4G"
	hAuto := Host{Name: "box", Sandbox: &HostSandbox{Memory: "auto"}}
	sizes, err = ResolveSandboxSizes(s4G, hAuto)
	if err != nil {
		t.Fatalf("ResolveSandboxSizes host auto: %v", err)
	}
	if sizes.Memory != MemoryAuto {
		t.Fatalf("host.sandbox.memory=auto -> %d, want MemoryAuto (%d)", sizes.Memory, MemoryAuto)
	}
	resolvedArgs, err = withMemoryMax(NewArgs{MemoryMax: sizes.Memory})
	if err != nil || resolvedArgs.MemoryMax != 0 {
		t.Fatalf("withMemoryMax(host auto) = %+v, %v; want MemoryMax=0", resolvedArgs, err)
	}
}

func TestCopyInStoreRememberSavedForget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if got := SavedCopyIn("box", "abcd2345"); len(got) != 0 {
		t.Fatalf("initial SavedCopyIn = %v, want empty", got)
	}
	want := []string{"/tmp/one.txt:/mnt/one.txt", "/tmp/two.txt:/home/agent/two.txt"}
	if err := RememberCopyIn("box", "abcd2345", want); err != nil {
		t.Fatalf("RememberCopyIn: %v", err)
	}
	fi, err := os.Stat(copyInStorePath())
	if err != nil {
		t.Fatalf("stat copy-in store: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("copy-in store mode = %o, want 0600", perm)
	}
	got := SavedCopyIn("box", "abcd2345")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SavedCopyIn = %v, want %v", got, want)
	}
	if other := SavedCopyIn("other", "abcd2345"); len(other) != 0 {
		t.Fatalf("SavedCopyIn(other) = %v, want empty", other)
	}
	ForgetCopyIn("box", "abcd2345")
	if after := SavedCopyIn("box", "abcd2345"); len(after) != 0 {
		t.Fatalf("SavedCopyIn after ForgetCopyIn = %v, want empty", after)
	}
}
