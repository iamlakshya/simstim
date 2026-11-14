package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestArbitraryPrerequisiteNeverLaunchesNativeVerifyOrPresenceQuery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the native no-invocation recorder uses a POSIX script")
	}
	fixture := makeNativePull(t, "source.txt", "work", false)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "native-query")
	script := "#!/bin/sh\nfor word do case \"$word\" in verify|cat-file) printf queried > \"$PULL_QUERY_MARKER\";; esac; done\nexec \"$PULL_REAL_GIT\" \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PULL_REAL_GIT", realGit)
	t.Setenv("PULL_QUERY_MARKER", marker)
	peer := &pullPeerFixture{supported: true, data: []byte("# v2 git bundle\n-" + strings.Repeat("f", 40) + " arbitrary boundary\n" + fixture.tip + " " + fixture.ref + "\n\nPACKfixture")}
	peer.begin = func(_ context.Context, _ api.PullBeginArgs, p *api.PullExport) error {
		p.SeedHeads = []string{fixture.base}
		p.DefaultRef = fixture.ref
		return nil
	}
	_, err = pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target"})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "pull_protocol" {
		t.Fatal("arbitrary prerequisite reached native verification", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("untrusted object identity was queried on the laptop")
	}
	// The same recorder must observe a legitimate native verification.
	result, err := pullSandbox(t.Context(), nativePullPeer(fixture), "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target"})
	if err != nil || result == nil || !result.Moved {
		t.Fatal("native positive control did not run", result, err)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("the recorder's positive control was never invoked", err)
	}
}
