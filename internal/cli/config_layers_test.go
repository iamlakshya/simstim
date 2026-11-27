package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// layerFindings exercises the public file checker without reading a real
// config folder or repository. The descriptor still owns the actual schema.
func layerFindings(t *testing.T, name, body string) []finding {
	t.Helper()
	for _, f := range client.ConfigFiles() {
		if f.Name != name {
			continue
		}
		f.Path = filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(f.Path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		exists, found := checkFile(f)
		if !exists {
			t.Fatal("the fixture was not checked")
		}
		return found
	}
	t.Fatalf("no schema for %s", name)
	return nil
}

func TestConfigLayersKnownKeys(t *testing.T) {
	for _, tt := range []struct{ name, file, body string }{
		{"all", "config.toml", "[sandbox]\npreset = 'locked'\nsecrets = ['TOKEN']\n[network]\nunknown = 'deny'\nallow = ['api.example.com']\n[classifier]\njudge = 'none'\n"},
		{"profile", "config.toml", "[profiles.web.sandbox]\nwall = 'microvm'\n[profiles.web.network]\nallow = ['api.example.com']\n"},
		{"repository", "config.toml", "[repos.'example.com/team/app'.sandbox]\nsecrets = ['!TOKEN']\nenv = { LOG_LEVEL = 'debug' }\n[repos.'example.com/team/app'.network]\nunknown = 'ask'\n"},
		{"host", "hosts.toml", "[[host]]\nname = 'box'\nssh = 'box'\n[host.sandbox]\npaste = 'media'\n[host.network]\ndeny = ['blocked.example.com']\n[host.lock.network]\nunknown = 'deny'\n"},
		{"distinct profile names", "config.toml", "[profiles.web.network]\nunknown = 'deny'\n[profiles.Web.network]\nunknown = 'ask'\n"},
		{"explicit zero and false", "config.toml", "[sandbox]\ncpus = 0\ndocker = false\ncheckpoints = 0\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := layerFindings(t, tt.file, tt.body); len(got) != 0 {
				t.Fatalf("valid layer gives %+v", got)
			}
		})
	}
}

func TestConfigLayersFindings(t *testing.T) {
	for _, tt := range []struct {
		name, body, code, contains string
		line                       int
	}{
		{"unknown spelling", "[profiles.web.network]\nunknwon = 'deny'\n", "unknown_key", `unknown key "unknwon"`, 2},
		{"profile type", "profiles.web = 'locked'\n", "wrong_type", "must be a table", 1},
		{"profile scalar", "[profiles.web.network]\nunknown = 5\n", "wrong_type", "must be a string", 2},
		{"profile enum", "[profiles.web.network]\nunknown = 'maybe'\n", "bad_value", "network.unknown", 2},
		{"repository duration", "[repos.'example.com/team/app'.network]\nhold = 'forever'\n", "bad_value", "network.hold", 2},
		{"negative processors", "[sandbox]\ncpus = -1\n", "bad_value", "sandbox.cpus", 2},
		{"network target", "[network]\nallow = ['*']\n", "bad_value", "target", 2},
		{"alias", "[profiles.web.network]\nunknown = 'deny'\nUnknown = 'ask'\n", "bad_value", "both", 2},
		{"profile lock", "[profiles.web.lock]\nnetwork.unknown = 'deny'\n", "layer_key", "not allowed", 1},
		{"repository UI", "[repos.'example.com/team/app'.ui]\nmotion = 'off'\n", "layer_key", "not allowed", 1},
		{"file once", "[profiles.web.network]\nonce = true\n", "layer_key", "not allowed", 2},
		{"file tools", "[network]\ntools = ['files']\n", "layer_key", "not allowed", 2},
		{"lock allowance", "[lock.network]\nallow = ['api.example.com']\n", "layer_key", "cannot grant", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := layerFindings(t, "config.toml", tt.body)
			if len(got) != 1 {
				t.Fatalf("want one finding, got %+v", got)
			}
			f := got[0]
			if f.Code != tt.code || f.Line != tt.line || !strings.Contains(f.Message, tt.contains) {
				t.Fatalf("got %+v, want %s on line %d containing %q", f, tt.code, tt.line, tt.contains)
			}
		})
	}
}

// The checker must report each bad setting, not only the merge validator's
// first error, and keep each table's actual spelling in its line lookup.
func TestConfigLayersReportsEveryBadValue(t *testing.T) {
	body := "[profiles.web.NETWORK]\nUnknown = 'maybe'\nHold = '-1s'\n[profiles.web.SANDBOX]\nCPUs = -1\n"
	got := layerFindings(t, "config.toml", body)
	if len(got) != 3 {
		t.Fatalf("want three findings, got %+v", got)
	}
	for i, line := range []int{2, 3, 5} {
		if got[i].Code != "bad_value" || got[i].Line != line {
			t.Fatalf("finding %d: %+v", i, got[i])
		}
	}
}

func TestConfigLayersText(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	layers := []client.LayerDescription{
		{Kind: api.LayerAll, Source: "config.toml"},
		{Kind: api.LayerHost, Name: "box", Source: "hosts.toml"},
		{Kind: api.LayerRepo, Name: "example.com/team/app", Source: "config.toml"},
		{Kind: api.LayerProfile},
	}
	var b bytes.Buffer
	writeConfigLayers(&b, layers)
	want := "\nLayers for " + client.CleanPath(tildeHome(cwd)) + ":\n" +
		"  all sessions config.toml\n" +
		"  host         box: hosts.toml\n" +
		"  repository   example.com/team/app: config.toml\n" +
		"  profile      none\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
