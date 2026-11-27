package cli

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/client"
)

func secretConfigHome(t *testing.T) func(string, string) {
	t.Helper()
	write := configHome(t)
	home := os.Getenv("HOME")
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(home, strings.ToLower(key))
		if key == "XDG_CONFIG_HOME" {
			dir = filepath.Join(home, ".config")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	return write
}

func TestConfigCheckSecretUnionSharesCanonicalAcceptance(t *testing.T) {
	for _, row := range []struct {
		name, text, code, message string
		line                      int
	}{
		{"legacy", "[secrets]\nTOKEN = 'printf one'\n", "", "", 0},
		{"structured", "[secrets.TOKEN]\nRun = 'printf one'\nhosts = ['api.example.org']\nheader = 'authorization'\nformat = 'Bearer {value}'\non_hosts = []\n", "", "", 0},
		{"raw binding default", "[secrets.TOKEN]\nrun = 'printf one'\nhosts = ['api.example.org']\nheader = 'authorization'\n", "", "", 0},
		{"explicit empty format", "[secrets.TOKEN]\nrun = 'printf one'\nhosts = ['api.example.org']\nheader = 'authorization'\nformat = ''\n", "bad_value", "explicitly empty format", 1},
		{"ssh key", "[secrets.KEY]\nrun = 'printf one'\nkind = 'ssh-key'\non_hosts = ['Box', 'box']\n", "", "", 0},
		{"legacy empty", "[secrets]\nTOKEN = ' ' \n", "bad_value", `"TOKEN" in [secrets] has an empty command`, 2},
		{"missing command", "[secrets.TOKEN]\nkind = 'ssh-key'\n", "bad_value", "has an empty command", 1},
		{"wrong union", "[secrets]\nTOKEN = 17\n", "wrong_type", "command string or a definition table", 2},
		{"wrong run", "[secrets.TOKEN]\nrun = 17\n", "wrong_type", `"run" in [secrets.TOKEN] must be a string`, 2},
		{"wrong hosts", "[secrets.TOKEN]\nrun = 'printf one'\nhosts = [17]\n", "wrong_type", "must be an array of strings", 3},
		{"wrong saved hosts", "[secrets.TOKEN]\nrun = 'printf one'\non_hosts = [17]\n", "wrong_type", "must be an array of strings", 3},
		{"empty binding", "[secrets.TOKEN]\nrun = 'printf one'\nhosts = []\n", "bad_value", "empty hosts binding", 1},
		{"incomplete binding", "[secrets.TOKEN]\nrun = 'printf one'\nhosts = ['api.example.org']\n", "bad_value", "needs exact HTTPS hosts", 1},
		{"invalid kind", "[secrets.TOKEN]\nrun = 'printf one'\nkind = 'api-key'\n", "bad_value", "kind must be empty or ssh-key", 1},
		{"mixed kind", "[secrets.TOKEN]\nrun = 'printf one'\nkind = 'ssh-key'\nhosts = ['api.example.org']\nheader = 'authorization'\nformat = '{value}'\n", "bad_value", "cannot combine ssh-key", 1},
		{"reserved socket", "[secrets]\nSSH_AUTH_SOCK = 'printf one'\n", "bad_value", "cannot be a secret", 2},
		{"unused tier", "[secret_tiers]\nUNUSED = 'borrowed'\n", "bad_value", "is not a host tier", 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			secretConfigHome(t)("config.toml", row.text)
			report := checkConfig()
			settings, warning := client.ReadSettings()
			_, canonicalError := client.NormaliseSettingsSecrets(settings)
			usable := warning == "" && canonicalError == nil
			if report.OK != usable || report.OK != (row.code == "") {
				t.Fatalf("checker and canonical settings disagree: %+v, warning %q, error %v", report, warning, canonicalError)
			}
			if row.code == "" {
				if len(report.Findings) != 0 {
					t.Fatalf("valid union has findings: %+v", report.Findings)
				}
				return
			}
			if len(report.Findings) != 1 {
				t.Fatalf("got %d findings, want one: %+v", len(report.Findings), report.Findings)
			}
			f := report.Findings[0]
			if f.Code != row.code || f.Line != row.line || f.Severity != severityError || !strings.Contains(f.Message, row.message) {
				t.Fatalf("finding %+v, want %s line %d and %q", f, row.code, row.line, row.message)
			}
		})
	}
}

func TestConfigCheckSecretSchemaKeepsSuggestionsAliasesAndExactNames(t *testing.T) {
	write := secretConfigHome(t)
	write("config.toml", "[secrets.TOKEN]\nrun = 'printf one'\nhots = ['api.example.org']\n")
	report := checkConfig()
	if len(report.Findings) != 1 || report.Findings[0].Code != "unknown_key" || report.Findings[0].Line != 3 || !reflect.DeepEqual(report.Findings[0].Suggestions, []string{"hosts"}) {
		t.Fatalf("schema suggestion or line changed: %+v", report.Findings)
	}
	write("config.toml", "[secrets.TOKEN]\nRun = 'printf one'\nrun = 'printf two'\n")
	var first string
	for range 64 {
		report = checkConfig()
		if len(report.Findings) != 1 || report.Findings[0].Code != "bad_value" || report.Findings[0].Line != 3 || !strings.Contains(report.Findings[0].Message, `both "Run" and "run" set "run"`) {
			t.Fatalf("secret alias or its line changed: %+v", report.Findings)
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if first != "" && string(data) != first {
			t.Fatal("secret aliases produced a map-order-dependent report")
		}
		first = string(data)
	}
	write("config.toml", "[secrets]\nToken = {run = 'printf one', on_hosts = []}\nTOKEN = 'printf two'\n")
	settings, warning := client.ReadSettings()
	view, err := client.NormaliseSettingsSecrets(settings)
	if report = checkConfig(); !report.OK || len(report.Findings) != 0 || warning != "" || err != nil || len(view.Commands) != 2 || view.Commands["Token"] == view.Commands["TOKEN"] {
		t.Fatalf("free names were folded: %+v, %q, %v", report, warning, err)
	}
	if hosts, present := view.OnHosts["Token"]; !present || hosts == nil || len(hosts) != 0 {
		t.Fatal("present-empty saved-host restriction disappeared")
	}
	if _, present := view.OnHosts["TOKEN"]; present {
		t.Fatal("absent saved-host restriction was invented")
	}
}

func TestConfigCheckSecretUnknownFieldsAndUnusedErrorsNeverRunCommands(t *testing.T) {
	write := secretConfigHome(t)
	marker := filepath.Join(t.TempDir(), "command-must-not-run")
	command := "touch " + strconv.Quote(marker)
	for _, field := range []string{"value", "Store", "source", "prefix"} {
		write("config.toml", "[secrets.TOKEN]\nrun = "+strconv.Quote(command)+"\n"+field+" = 'command-value-marker'\n")
		report := checkConfig()
		if len(report.Findings) != 1 || report.Findings[0].Code != "unknown_key" || report.Findings[0].Line != 3 || strings.Contains(report.Findings[0].Message, "command-value-marker") {
			t.Fatalf("privileged field or safe diagnostic changed: %+v", report.Findings)
		}
	}
	write("config.toml", "[secrets]\nA = "+strconv.Quote(command)+"\nZ = {run = 'command-value-marker', kind = 'unknown'}\n[secret_tiers]\nUNUSED = 'borrowed'\n")
	report := checkConfig()
	if report.OK || len(report.Findings) != 2 || report.Findings[0].Line != 3 || report.Findings[1].Line != 5 {
		t.Fatalf("unused definition or tier escaped global report: %+v", report.Findings)
	}
	for _, f := range report.Findings {
		if f.Code != "bad_value" || strings.Contains(f.Message, "command-value-marker") || strings.Contains(f.Message, command) {
			t.Fatalf("semantic finding leaked a command or value: %+v", f)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("config check executed a secret command: %v", err)
	}
}

// A free-map spelling must select only its own trusted provider defaults.
func TestConfigCheckSecretNamesKeepCanonicalDefaultIdentity(t *testing.T) {
	defaults, err := agents.GlobalSecretDefaults()
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, candidate := range slices.Sorted(maps.Keys(defaults)) {
		if strings.ToLower(candidate) != candidate && defaults[candidate].Bind.Header != "" {
			name = candidate
			break
		}
	}
	if name == "" {
		t.Fatal("the canonical agent data has no case-sensitive HTTP secret default")
	}
	for _, spelling := range []string{name, strings.ToLower(name)} {
		t.Run(spelling, func(t *testing.T) {
			write := secretConfigHome(t)
			write("config.toml", "[secrets."+strconv.Quote(spelling)+"]\nrun = 'printf one'\nkind = 'ssh-key'\n")
			report := checkConfig()
			settings, warning := client.ReadSettings()
			_, err := client.NormaliseSettingsSecrets(settings)
			want := spelling != name // The exact HTTP default cannot become an SSH key.
			if warning != "" || report.OK != want || report.OK != (err == nil) {
				t.Fatalf("free name selected another default: %+v, warning %q, error %v", report, warning, err)
			}
		})
	}
}
