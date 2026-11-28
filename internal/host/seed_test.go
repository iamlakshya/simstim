package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestResolveSessionLimitsAndIdentityEnv(t *testing.T) {
	// Walled sessions default to agent table / 2 CPUs + 4 GiB when omitted.
	lim := resolveSessionLimits(0, 0, 10<<30, "claude", true)
	if lim.CPUs != 2 {
		t.Errorf("walled default CPUs = %d, want 2", lim.CPUs)
	}
	if lim.DiskMax != 10<<30 {
		t.Errorf("walled DiskMax = %d, want 10 GiB", lim.DiskMax)
	}
	// Explicit sizes override defaults.
	lim = resolveSessionLimits(512<<20, 4, 0, "claude", true)
	if lim.CPUs != 4 || lim.CPUsAsked != 4 || lim.MemoryAsked != 512<<20 {
		t.Errorf("explicit limits = %+v", lim)
	}

	// Identity vars win over user env, and walled sessions receive BROWSER.
	userEnv := map[string]string{
		"LOG_LEVEL":          "debug",
		brand.Env("SESSION"): "spoofed",
		brand.Env("HOST"):    "spoofed",
		"BROWSER":            "/usr/bin/fake",
	}
	got := applyUserAndIdentityEnv([]string{"PATH=/usr/bin", "LOG_LEVEL=info"}, userEnv, "abcd2345", "my-box", "claude", "auth-fix", true)
	m := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL = %q, want debug", m["LOG_LEVEL"])
	}
	if m[brand.Env("SESSION")] != "abcd2345" || m[brand.Env("HOST")] != "my-box" ||
		m[brand.Env("AGENT")] != "claude" || m[brand.Env("SANDBOX")] != "auth-fix" {
		t.Errorf("identity env = %+v", m)
	}
	wantBrowser := filepath.Join(paths.GuestRuntimeDir("abcd2345"), "browser")
	if m["BROWSER"] != wantBrowser {
		t.Errorf("BROWSER = %q, want %q", m["BROWSER"], wantBrowser)
	}
}

func TestResolvePullRepoDir(t *testing.T) {
	m := &Manifest{
		ID:   "abcd2345",
		Name: "multi",
		Cwd:  "/tmp/work",
		SeedRepos: []api.SeedRepoRecord{
			{Name: "api", Dir: "/tmp/work/api", Branch: "main"},
			{Name: "web", Dir: "/tmp/work/web", Branch: "main"},
		},
	}
	// Ambiguous without --repo.
	_, err := resolvePullRepoDir(m, "")
	if err == nil || !strings.Contains(err.Error(), "2 repositories") {
		t.Fatalf("resolvePullRepoDir('') = %v, want repo_ambiguous", err)
	}
	// Explicit --repo selects matching clone.
	dir, err := resolvePullRepoDir(m, "web")
	if err != nil || dir != "/tmp/work/web" {
		t.Fatalf("resolvePullRepoDir('web') = %q, %v", dir, err)
	}
	// Unknown --repo reports repo_not_found.
	_, err = resolvePullRepoDir(m, "nope")
	if err == nil || !strings.Contains(err.Error(), "no repository named") {
		t.Fatalf("resolvePullRepoDir('nope') = %v, want repo_not_found", err)
	}
}

func TestApplyStagedCopiesWritesWalledHomeAndAudit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))

	id := "abcd2345"
	stageDir := paths.StageDir(id)
	if err := os.MkdirAll(filepath.Join(stageDir, "copy", "0", "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "copy", "0", "skills", "tip.md"), []byte("hello skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		ID:   id,
		Name: "walled-copy",
		Cwd:  paths.WorkDir(id),
		Wall: &WallRecord{Level: "none", Driver: "plain"},
	}
	if err := applyStagedCopies(m, stageDir, []api.CopyInFile{
		{StageRel: "copy/0/skills", GuestPath: "/home/agent/.claude/skills", IsDir: true},
	}); err != nil {
		t.Fatalf("applyStagedCopies: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(paths.SessionDir(id), "home", ".claude", "skills", "tip.md"))
	if err != nil || string(got) != "hello skill" {
		t.Fatalf("copied skill = %q, err=%v", got, err)
	}
	aud, err := os.ReadFile(paths.AuditLog(id))
	if err != nil || !strings.Contains(string(aud), `"event":"copy_in"`) || !strings.Contains(string(aud), "/home/agent/.claude/skills/tip.md") {
		t.Fatalf("audit log = %q, err=%v", aud, err)
	}
	if !sessionOps["session.start"] || !sessionOps["copy.apply"] {
		t.Errorf("session.start and copy.apply must be refused inside sessions via sessionOps")
	}
}

func TestAttachSeedWorkspacesMultiRepoSandboxed(t *testing.T) {
	for _, driver := range []string{"process", "gvisor"} {
		t.Run(driver, func(t *testing.T) {
			workBase := filepath.Join(t.TempDir(), "work")
			apiDir := filepath.Join(workBase, "api")
			webDir := filepath.Join(workBase, "web")
			m := &Manifest{
				ID:   "seed2345",
				Name: "duo",
				Cwd:  "/work/scratch",
				Wall: &WallRecord{
					Level:          driver,
					Driver:         driver,
					WorkspaceHost:  workBase,
					WorkspaceGuest: "/work/scratch",
				},
			}
			records := []api.SeedRepoRecord{
				{Name: "api", Dir: apiDir, Branch: "main"},
				{Name: "web", Dir: webDir, Branch: "main"},
			}
			m.SeedRepos = records
			attachSeedWorkspaces(m, workBase, records)

			if m.Wall.WorkspaceHost != apiDir || m.Wall.WorkspaceGuest != "/work/api" || m.Cwd != "/work/api" {
				t.Fatalf("primary workspace = host:%q guest:%q cwd:%q, want %q /work/api",
					m.Wall.WorkspaceHost, m.Wall.WorkspaceGuest, m.Cwd, apiDir)
			}
			if len(m.Wall.ExtraRW) != 1 || m.Wall.ExtraRW[0].Source != webDir || m.Wall.ExtraRW[0].Dest != "/work/web" {
				t.Fatalf("ExtraRW = %+v, want [{Source:%q Dest:/work/web}]", m.Wall.ExtraRW, webDir)
			}
			if got, err := resolvePullRepoDir(m, "web"); err != nil || got != webDir {
				t.Fatalf("resolvePullRepoDir(web) = %q, %v", got, err)
			}
			if got, err := resolvePullRepoDir(m, "api"); err != nil || got != apiDir {
				t.Fatalf("resolvePullRepoDir(api) = %q, %v", got, err)
			}
		})
	}
}

func TestSeedCloneHandlesDashValues(t *testing.T) {
	tmp := t.TempDir()
	srcRepo := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester",
			"GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Tester",
			"GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit(srcRepo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(srcRepo, "a.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(srcRepo, "add", ".")
	runGit(srcRepo, "commit", "-m", "init")
	bundle := filepath.Join(tmp, "src.bundle")
	runGit(srcRepo, "bundle", "create", bundle, "main")

	destDir := filepath.Join(tmp, "cloned")
	res, err := seedClone(&seedCloneArgs{
		BundlePath: bundle,
		DestDir:    destDir,
		Branch:     "main",
		Origin:     "https://example.invalid/org/src.git",
		UserName:   "-dash-user",
		UserEmail:  "-dash@example.com",
	})
	if err != nil || res.Head == "" {
		t.Fatalf("seedClone with leading-dash config values: res=%+v err=%v", res, err)
	}
	if got := runGit(destDir, "config", "user.name"); got != "-dash-user" {
		t.Errorf("user.name = %q, want -dash-user", got)
	}
	if got := runGit(destDir, "config", "user.email"); got != "-dash@example.com" {
		t.Errorf("user.email = %q, want -dash@example.com", got)
	}
}

func TestApplyCopyInKeepsPendingWhenCopyFilesEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))

	id := "pend2345"
	m := &Manifest{
		Schema:        1,
		ID:            id,
		Name:          "pending",
		Cwd:           home,
		CopyIn:        []api.CopyInSpec{{Source: "/tmp/a.txt", Guest: "/mnt/a.txt"}},
		CopiesPending: true,
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCopyIn(api.CopyApplyArgs{ID: id}); err != nil {
		t.Fatalf("ApplyCopyIn empty: %v", err)
	}
	after, err := LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.CopiesPending {
		t.Fatal("ApplyCopyIn silently cleared CopiesPending when m.CopyIn is non-empty and CopyFiles is nil")
	}
}

func TestFilterSandboxEnvProtectsIdentityAndBrowser(t *testing.T) {
	sandboxEnv := map[string]string{
		"GOPROXY":            "https://sandbox.example",
		"FROM_SANDBOX":       "1",
		"BROWSER":            "/bin/evil-sandbox",
		brand.Env("HOST"):    "spoof-sandbox",
		brand.Env("SANDBOX"): "spoof-sandbox",
	}
	sessionEnv := map[string]string{
		"GOPROXY":            "https://session.example",
		"FROM_SESSION":       "2",
		"BROWSER":            "/bin/evil-session",
		brand.Env("AGENT"):   "spoof-session",
		brand.Env("SESSION"): "spoof-session",
	}
	got := filterSandboxEnv(sessionEnv, sandboxEnv)
	if got["GOPROXY"] != "https://session.example" {
		t.Errorf("GOPROXY = %q, want session override https://session.example", got["GOPROXY"])
	}
	if got["FROM_SANDBOX"] != "1" || got["FROM_SESSION"] != "2" {
		t.Errorf("non-protected vars = %+v", got)
	}
	for _, blocked := range []string{"BROWSER", brand.Env("HOST"), brand.Env("SANDBOX"), brand.Env("AGENT"), brand.Env("SESSION")} {
		if v, exists := got[blocked]; exists {
			t.Errorf("protected var %s leaked into SandboxEnv with value %q", blocked, v)
		}
	}
}
