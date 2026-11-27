//go:build darwin || linux

package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func metadataNameSourceFixture(t *testing.T, config string) (*localGitMetadataNames, *agentSettingsSnapshot) {
	t.Helper()
	nativeLogTestEnv(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := agentModeForTest(t)
	snapshot, err := openAgentSettingsSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.close() })
	source, err := newLocalGitMetadataNames(t.Context(), mode.inspector, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source, snapshot
}

func TestMetadataNameSourceValidatesAllDefinitionsBeforeCollision(t *testing.T) {
	for _, config := range []string{
		"[secrets]\nPATH='printf command-must-never-run'\n",
		"[secrets]\nPATH='printf command-must-never-run'\nZ_UNUSED={run='printf unused',hosts=[]}\n",
	} {
		// Each fixture uses actual config bytes and the real mode/Inspector.
		// The ordinary canonical normaliser is the independent error oracle.
		source, _ := metadataNameSourceFixture(t, config)
		want := ValidateSettings(LoadSettings())
		got := source.CheckKeys(t.Context(), []string{"PATH"})
		var failure *UserError
		if !errors.As(got, &failure) {
			t.Fatal("missing actual namespace refusal:", got)
		}
		if want == nil {
			if failure.Code != "git_environment_secret" {
				t.Fatal("valid collision was not classified precisely:", got)
			}
		} else {
			var canonical *UserError
			if !errors.As(want, &canonical) || failure.Code != canonical.Code || failure.Kind != canonical.Kind {
				t.Fatalf("collision masked canonical unused-definition failure: got %v want %v", got, want)
			}
		}
	}
}

func TestMetadataNameSourceRevokesBorrowAndChecksOriginalFD(t *testing.T) {
	source, snapshot := metadataNameSourceFixture(t, "[secrets]\nOTHER='printf not-executed'\n")
	if err := source.CheckKeys(t.Context(), []string{"PATH", "LC_ALL"}); err != nil {
		t.Fatal("noncolliding metadata namespace did not validate:", err)
	}
	if err := source.Revalidate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.CheckKeys(t.Context(), []string{"PATH"}); err == nil {
		t.Fatal("a closed metadata borrower retained namespace authority")
	}
	if err := snapshot.check(); err != nil {
		t.Fatal("closing the borrower closed the original operation's source:", err)
	}
	mode := agentModeForTest(t)
	freshBorrow, err := newLocalGitMetadataNames(t.Context(), mode.inspector, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer freshBorrow.Close()
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nPATH='printf changed-name-source'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := freshBorrow.Revalidate(t.Context()); err == nil {
		t.Fatal("metadata revalidation adopted a changed original settings source")
	}
}
