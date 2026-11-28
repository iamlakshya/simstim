package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestShellAndExecCLI(t *testing.T) {
	walled := client.Session{
		ID: "s2a3b4c5", Name: "auth-fix", Agent: "claude", Cwd: "/work/app",
		State: api.StateWorking, Alive: true,
		Wall: &api.WallInfo{Level: "process", Driver: "process"},
	}
	stopped := client.Session{
		ID: "s3a3b4c5", Name: "stopped-sb", Agent: "claude", Cwd: "/work/app",
		State: api.StateStopped, Alive: false,
		Wall: &api.WallInfo{Level: "process", Driver: "process"},
	}
	unwalled := client.Session{
		ID: "s4a3b4c5", Name: "plain-one", Agent: "claude", Cwd: "/home/dev/app",
		State: api.StateWorking, Alive: true,
	}
	_, tr := withFake(t, []client.Session{walled, stopped, unwalled})
	tr.Features = append(tr.Features, "spawn")
	if err := saveHost(t.Context(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}

	// 1. Synonym `ssh` suggests `shell` (CLI spec 4.3).
	got, code := lineErr(t, "ssh", "box/"+walled.ID)
	if code != 2 || !strings.Contains(got, `Did you mean "shell"?`) || !strings.Contains(got, brand.Name+" shell box/"+walled.ID) {
		t.Errorf("ssh synonym: exit %d\n%s", code, got)
	}

	// 2. `exec` pre-command errors exit with 125 while printing Error: and Try: on stderr.
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing command", []string{"exec", "box/auth-fix"}, "needs a command after --"},
		{"stopped sandbox", []string{"exec", "box/stopped-sb", "--", "true"}, "is stopped"},
		{"unwalled session", []string{"exec", "box/plain-one", "--", "true"}, "has no sandbox wall"},
		{"unknown flag", []string{"exec", "--no-such-flag", "box/auth-fix", "--", "true"}, "has no flag"},
	} {
		out, errOut, ec := mainRun(t, tc.args...)
		if ec != 125 || out != "" || !strings.Contains(errOut, tc.want) || !strings.Contains(errOut, "Try:") {
			t.Errorf("%s: exit %d (want 125)\nstdout=%q\nstderr=%q", tc.name, ec, out, errOut)
		}
	}

	// 3. `exec` propagates command exit codes (0, 1, 124, 126, 127) and separates stdout/stderr.
	oldExec := execRunner
	t.Cleanup(func() { execRunner = oldExec })
	for _, tc := range []struct {
		retCode int
		retErr  error
		want    int
	}{
		{0, nil, 0},
		{1, nil, 1},
		{124, nil, 124},
		{126, nil, 126},
		{127, nil, 127},
		{125, errors.New("stream died"), 125},
	} {
		execRunner = func(_ context.Context, _ client.Host, a api.ExecArgs, _ io.Reader, stdout, stderr io.Writer) (int, error) {
			if a.ID != walled.ID {
				t.Errorf("exec got ID %q, want %q", a.ID, walled.ID)
			}
			_, _ = io.WriteString(stdout, "OUT_OK\n")
			_, _ = io.WriteString(stderr, "ERR_OK\n")
			return tc.retCode, tc.retErr
		}
		out, errOut, ec := mainRun(t, "exec", "box/auth-fix", "--", "echo", "hi")
		if ec != tc.want {
			t.Errorf("retCode=%d err=%v: exit %d, want %d\nstderr=%s", tc.retCode, tc.retErr, ec, tc.want, errOut)
		}
		if tc.retErr == nil && (!strings.Contains(out, "OUT_OK") || !strings.Contains(errOut, "ERR_OK")) {
			t.Errorf("stdout/stderr mismatch: out=%q err=%q", out, errOut)
		}
	}

	// 3b. `exec --timeout 0` disables the command timeout (NoTimeout=true, TimeoutMS=0),
	// while a positive timeout passes TimeoutMS with NoTimeout=false.
	var gotArgs api.ExecArgs
	execRunner = func(_ context.Context, _ client.Host, a api.ExecArgs, _ io.Reader, _, _ io.Writer) (int, error) {
		gotArgs = a
		return 0, nil
	}
	if _, errOut, ec := mainRun(t, "exec", "box/auth-fix", "--timeout", "0", "--", "true"); ec != 0 {
		t.Fatalf("exec --timeout 0 failed: exit %d\n%s", ec, errOut)
	}
	if !gotArgs.NoTimeout || gotArgs.TimeoutMS != 0 {
		t.Fatalf("exec --timeout 0 sent %+v, want NoTimeout=true TimeoutMS=0", gotArgs)
	}
	if _, errOut, ec := mainRun(t, "exec", "box/auth-fix", "--timeout", "2s", "--", "true"); ec != 0 {
		t.Fatalf("exec --timeout 2s failed: exit %d\n%s", ec, errOut)
	}
	if gotArgs.NoTimeout || gotArgs.TimeoutMS != 2000 {
		t.Fatalf("exec --timeout 2s sent %+v, want NoTimeout=false TimeoutMS=2000", gotArgs)
	}

	// 4. `shell --detach --json` spawns inside sandbox (and reuses running shell without --new).
	out, errOut, ec := mainRun(t, "shell", "box/auth-fix", "--detach", "--json")
	if ec != 0 || errOut != "" {
		t.Fatalf("shell --detach --json: exit %d\nstdout=%s\nstderr=%s", ec, out, errOut)
	}
	var sobj map[string]any
	if err := json.Unmarshal([]byte(out), &sobj); err != nil || sobj["started"] != true {
		t.Fatalf("shell json: %v %s", err, out)
	}

	// 5. `new --in` rejects sandbox-changing flags with exit code 2 (usage error).
	for _, bad := range [][]string{
		{"new", "box", "--in", "auth-fix", "--wall", "none", "--detach"},
		{"new", "box", "--in", "auth-fix", "--dir", "/tmp", "--detach"},
		{"new", "box", "--in", "auth-fix", "--cpus", "2", "--detach"},
		{"new", "box", "--in", "auth-fix", "--memory", "2G", "--detach"},
		{"new", "box", "--in", "auth-fix", "--memory-max", "2G", "--detach"},
		{"new", "box", "--in", "auth-fix", "--copy", "/tmp", "--detach"},
		{"new", "box", "--in", "auth-fix", "--seed", ".", "--detach"},
		{"new", "box", "--in", "auth-fix", "--seed-dirty", "--detach"},
		{"new", "box", "--in", "auth-fix", "--image", "debian:trixie", "--detach"},
		{"new", "box", "--in", "auth-fix", "--keep-system", "--detach"},
		{"new", "box", "--in", "auth-fix", "--docker", "--detach"},
		{"new", "box", "--in", "auth-fix", "--mount", "/tmp", "--detach"},
	} {
		_, errOut, ec := mainRun(t, bad...)
		if ec != 2 {
			t.Errorf("%v: exit %d, want 2\n%s", bad, ec, errOut)
		}
	}
}

func TestNewInSkipsCopyInStagingAndKeepsSecrets(t *testing.T) {
	walled := client.Session{
		ID: "s2a3b4c5", Name: "auth-fix", Agent: "claude", Cwd: "/work/app",
		State: api.StateWorking, Alive: true,
		Wall: &api.WallInfo{Level: "process", Driver: "process"},
	}
	_, tr := withFake(t, []client.Session{walled})
	tr.Features = append(tr.Features, "spawn", "secrets", api.FeatureSFTP, api.FeatureSeed)
	if err := saveHost(t.Context(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}

	copySrc := filepath.Join(t.TempDir(), "skill.txt")
	if err := os.WriteFile(copySrc, []byte("laptop-skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, "[sandbox]\ncopy_in = [\""+filepath.ToSlash(copySrc)+"\"]\n\n[secrets]\nJOIN_TOKEN = \"echo join-secret-val\"\n")

	var gotNew api.NewArgs
	var sawNew bool
	tr.OnNew = func(a api.NewArgs) {
		gotNew = a
		sawNew = true
	}

	out, errOut, ec := mainRun(t, "new", "box", "--in", "auth-fix", "--secret", "JOIN_TOKEN", "--detach", "--json")
	if ec != 0 || errOut != "" {
		t.Fatalf("new --in with copy_in configured: exit %d\nstdout=%s\nstderr=%s", ec, out, errOut)
	}
	if !sawNew {
		t.Fatal("expected bridge to receive a new request")
	}
	if gotNew.In != walled.ID {
		t.Fatalf("new.In = %q, want %q", gotNew.In, walled.ID)
	}
	if gotNew.DeferStart {
		t.Fatal("new --in set DeferStart=true despite joining an existing sandbox")
	}
	if gotNew.Secrets["JOIN_TOKEN"] != "join-secret-val" {
		t.Fatalf("new --in dropped secret: got %+v", gotNew.Secrets)
	}
	for _, op := range tr.Requests() {
		if op == "session.start" {
			t.Fatalf("new --in unexpectedly called StartDeferred (requests=%v)", tr.Requests())
		}
	}
}

func TestConfigCheckRefusesKeepStartupFileAndExitCodesListsExec(t *testing.T) {
	cfgDir := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	appDir := filepath.Join(cfgDir, brand.Name)
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(appDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[sandbox]\nkeep = [\".bash_history\", \".bashrc\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, ec := mainRun(t, "config", "check", "--json")
	if ec != 1 {
		t.Fatalf("config check exit = %d, want 1\n%s", ec, out)
	}
	if !strings.Contains(out, `"code": "keep_refused"`) && !strings.Contains(out, `"code":"keep_refused"`) {
		t.Fatalf("config check missing keep_refused:\n%s", out)
	}

	// `help exit-codes` must include the CLI spec 5.2 exec table.
	helpOut := helpOutput(t, "help", "exit-codes")
	for _, want := range []string{"124", "125", "126", "127", "128 + N"} {
		if !strings.Contains(helpOut, want) {
			t.Errorf("help exit-codes missing %q:\n%s", want, helpOut)
		}
	}
}
