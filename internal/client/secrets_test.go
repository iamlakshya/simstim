package client

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const testSecret = "fake-secret-for-tests-9876543210"

func TestFetchSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are sh")
	}
	var s Settings
	s.Secrets = map[string]string{
		"OPENAI_API_KEY": "printf '%s\\n' " + testSecret,
		"MULTI":          "printf 'line one\\nline two\\r\\n'",
	}
	got, err := FetchSecrets(context.Background(), s, []string{"OPENAI_API_KEY", "MULTI"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["OPENAI_API_KEY"]) != testSecret || string(got["MULTI"]) != "line one\nline two" {
		t.Fatalf("values: %q %q", string(got["OPENAI_API_KEY"]), string(got["MULTI"]))
	}
	if strings.Contains(fmt.Sprint(got), testSecret) {
		t.Fatal("the values print")
	}
}

func TestFetchSecretsErrorsShowNoValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are sh")
	}
	old := SecretTimeout
	SecretTimeout = 300 * time.Millisecond
	defer func() { SecretTimeout = old }()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var s Settings
	s.Secrets = map[string]string{
		"FAILS":   "echo " + testSecret + "; echo " + testSecret + " >&2; exit 3",
		"EMPTY":   "true",
		"SLOW":    "echo " + testSecret + "; sleep 5",
		"NUL":     "printf '" + testSecret + "\\000'",
		"TOO_BIG": "head -c 70000 /dev/zero | tr '\\0' x",
	}
	for name, want := range map[string]string{
		"FAILS":   "the command of the secret FAILS failed (exit code 3)",
		"EMPTY":   "the command of the secret EMPTY printed nothing",
		"SLOW":    "the command of the secret SLOW did not finish in 300ms",
		"NUL":     "the command of the secret NUL printed a NUL byte",
		"TOO_BIG": "the command of the secret TOO_BIG printed more than 64 KiB",
		"MISSING": "no secret named MISSING in config.toml",
		"BAD-ONE": `"BAD-ONE" cannot name a secret`,
	} {
		_, err := FetchSecrets(context.Background(), s, []string{name}, false)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Msg != want {
			t.Errorf("%s: %v, want %q", name, err, want)
			continue
		}
		if strings.Contains(ue.Msg+ue.Hint, testSecret) {
			t.Errorf("%s: the error shows the value: %+v", name, ue)
		}
		if want := "Add MISSING = \"<the command that prints it>\" under [secrets] in " + settingsPath() + "."; name == "MISSING" && ue.Hint != want {
			t.Errorf("MISSING: hint %q, want %q", ue.Hint, want)
		}
	}
}

func TestSecretNames(t *testing.T) {
	h := Host{Name: "h", Secrets: []string{"A", "B"}}
	if got := strings.Join(SecretNames(h, []string{"B", "C", " "}), ","); got != "A,B,C" {
		t.Fatalf("names %s", got)
	}
}

func TestRequireSecrets(t *testing.T) {
	c := &Conn{Host: Host{Name: "old"}}
	if err := c.RequireSecrets(NewArgs{}); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireSecrets(NewArgs{Secrets: map[string]api.Secret{"A": "x"}}); err == nil {
		t.Fatal("an old helper got secrets it would drop")
	}
	c.Features = []string{"secrets"}
	if err := c.RequireSecrets(NewArgs{Secrets: map[string]api.Secret{"A": "x"}}); err != nil {
		t.Fatal(err)
	}
}
