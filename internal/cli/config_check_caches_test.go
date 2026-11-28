package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestConfigCheckSandboxCaches(t *testing.T) {
	t.Run("valid keep_caches and caches in config.toml and hosts.toml", func(t *testing.T) {
		hosttest.New(t)
		writeConfigFiles(t,
			"[sandbox]\nkeep_caches = true\ncaches = [\"!.gradle/caches\", \".cache/ccache\"]\n",
			"[[host]]\nname = \"box\"\nssh = \"dev@box\"\n[host.sandbox]\nkeep_caches = false\ncaches = [\"!.npm/_cacache\", \".cache/bazel\"]\n",
		)
		out, errOut, code := runConfigCheckMain(t, "config", "check")
		if code != 0 || errOut != "" || strings.Count(out, "no problems") != 2 {
			t.Fatalf("valid caches: code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	})

	t.Run("rejects .cargo/bin in config.toml and host.sandbox.caches in hosts.toml", func(t *testing.T) {
		hosttest.New(t)
		writeConfigFiles(t,
			"[sandbox]\ncaches = [\".cargo/bin\"]\n",
			"[[host]]\nname = \"box\"\nssh = \"dev@box\"\n[host.sandbox]\ncaches = [\"../outside\", \".bashrc\"]\n",
		)
		jout, jerr, jcode := runConfigCheckMain(t, "config", "check", "--json")
		if jcode != 1 || jerr != "" {
			t.Fatalf("invalid caches: code=%d stdout=%q stderr=%q", jcode, jout, jerr)
		}
		checkJSON(t, "config check --json (bad caches)", jout, false)
		var rep configReport
		if err := json.Unmarshal([]byte(jout), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.OK || len(rep.Findings) != 3 {
			t.Fatalf("findings = %+v, want 3 findings", rep.Findings)
		}
		for _, f := range rep.Findings {
			if f.Code != "cache_path_invalid" || f.Severity != "error" || f.Line == 0 {
				t.Errorf("unexpected finding: %+v", f)
			}
		}
		if !strings.Contains(rep.Findings[0].Message, ".cargo/bin") {
			t.Errorf("finding[0] = %+v, want .cargo/bin message", rep.Findings[0])
		}
	})
}
