package cli

import (
	"strings"
	"testing"
)

func TestConfigCheckSecretTables(t *testing.T) {
	valid := `
[secrets]
PLAIN = "echo ok"

[secrets.ANTHROPIC_API_KEY]
run = "security find-generic-password -s anthropic-api-key -w"
hosts = ["api.anthropic.com"]
header = "x-api-key"
format = "{value}"

[secrets.ACME_KEY]
command = "op read op://dev/acme/key"
hosts = ["api.acme.example"]
route = "acme"
`
	writeConfigFiles(t, valid, "")
	out, errOut, code := runConfigCheckMain(t, "config", "check")
	if code != 0 || errOut != "" || !strings.Contains(out, "no problems") {
		t.Fatalf("valid secret tables: code=%d stdout=%q stderr=%q", code, out, errOut)
	}

	invalid := `
[secrets]
NUM_SECRET = 42

[secrets.BAD_HOST]
run = "echo x"
hosts = ["https://api.example.com"]

[secrets.BAD_KEY]
run = "echo x"
bogus = true

[secrets.EMPTY_TABLE]
hosts = ["api.example.com"]

[secrets.MULTI_ROUTE]
run = "echo x"
hosts = ["a.example.com", "b.example.com"]
route = "shared"

[secrets.CRLF_FMT]
run = "echo x"
hosts = ["api.example.com"]
format = "Bearer {value}\r\nX-Inject: 1"
`
	writeConfigFiles(t, invalid, "")
	out, _, code = runConfigCheckMain(t, "config", "check")
	if code != 1 {
		t.Fatalf("invalid secret tables: code=%d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		`secret "NUM_SECRET" in [secrets] must be a command string`,
		`[secrets.BAD_HOST] hosts:`,
		`unknown key "bogus" in [secrets.BAD_KEY]`,
		`secret "EMPTY_TABLE" in [secrets] has an empty command`,
		`cannot set route "shared" with multiple hosts`,
		`format cannot contain CR or LF characters`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config check output missing %q:\n%s", want, out)
		}
	}
}
