package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestConfigChecksEveryUnusedProfileAndRepositoryHostReference(t *testing.T) {
	write := secretConfigHome(t)
	write("hosts.toml", "[[host]]\nname='box'\nssh='box.example'\n")
	write("config.toml", "[profiles.unused.sandbox]\nhost=['box','missing']\n[repos.'forge.example/team/*'.sandbox]\nhost='gone'\n")
	exists, findings := checkFile(client.ConfigFiles()[0])
	if !exists || len(findings) != 2 {
		t.Fatal(exists, findings)
	}
	for i, name := range []string{"missing", "gone"} {
		if findings[i].Code != "host_not_found" || findings[i].Line != 2+i*2 || !strings.Contains(findings[i].Message, name) {
			t.Fatal(findings)
		}
	}
}

func TestConfigHostReferencesKeepCanonicalAliasesAndAuto(t *testing.T) {
	write := secretConfigHome(t)
	write("hosts.toml", "[[host]]\nname='box'\nssh='box.example'\n")
	write("config.toml", "[PROFILES.unused.SANDBOX]\nHOST='box'\n[repos.'forge.example/team/*'.sandbox]\nhost='auto'\n")
	_, findings := checkFile(client.ConfigFiles()[0])
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	write("config.toml", "[PROFILES.unused.SANDBOX]\nHOST='missing'\n")
	_, findings = checkFile(client.ConfigFiles()[0])
	if len(findings) != 1 || findings[0].Code != "host_not_found" || findings[0].Line != 2 {
		t.Fatal(findings)
	}
}

func TestConfigHostReferenceDoesNotDuplicateTypeOrLayerErrors(t *testing.T) {
	write := secretConfigHome(t)
	write("config.toml", "[sandbox]\nhost='missing'\n[profiles.unused.sandbox]\nhost=42\n")
	_, findings := checkFile(client.ConfigFiles()[0])
	if len(findings) != 2 || findings[0].Code != "host_rule_layer" || findings[1].Code != "bad_value" {
		t.Fatal(findings)
	}
}

func TestConfigHostReferenceNeverRunsASecretReader(t *testing.T) {
	write := secretConfigHome(t)
	marker := filepath.Join(os.Getenv("HOME"), "reader-ran")
	write("hosts.toml", "[[host]]\nname='box'\nssh='box.example'\n")
	write("config.toml", "[secrets]\nTOKEN=\"touch "+client.ShellWord(marker)+"\"\n[profiles.unused.sandbox]\nhost='missing'\n")
	_, findings := checkFile(client.ConfigFiles()[0])
	if len(findings) != 1 || findings[0].Code != "host_not_found" {
		t.Fatal(findings)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("config reference check ran a value reader", err)
	}
}
