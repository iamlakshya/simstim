package cli

import "testing"

func TestCopyAddressSeparatesWindowsAndRootedSandboxPaths(t *testing.T) {
	for _, value := range []string{`C:\work\notes.md`, `D:/work/notes.md`, `\\server\share\notes.md`, `./folder/name:local`, `/tmp/name:local`} {
		if _, _, remote, err := copyAddress(value); err != nil || remote {
			t.Errorf("local path %q became remote: %v", value, err)
		}
	}
	for value, want := range map[string]string{"box/work:~/notes.md": "/home/agent/notes.md", "box/work:~/": "/home/agent/", "box/work:/work/project/": "/work/project/"} {
		address, guest, remote, err := copyAddress(value)
		if err != nil || !remote || address != "box/work" || guest != want {
			t.Errorf("%q = %q %q %v %v", value, address, guest, remote, err)
		}
	}
	for _, value := range []string{"box/work:relative", "box/work:/etc/shadow", "box/work:/work/a/../b", "box/work:/work/.git/config"} {
		if _, _, _, err := copyAddress(value); err == nil {
			t.Errorf("unsafe copy path accepted: %q", value)
		}
	}
}

func TestCopyFlagsKeepExplicitReplacementAndReviewChoices(t *testing.T) {
	cmd := cpCmd()
	if flag := cmd.Flags().Lookup("force"); flag == nil || flag.Shorthand != "f" || len(flag.Annotations[sharedKey]) != 1 {
		t.Fatal("cp force does not use the canonical shared meaning")
	}
	if cmd.Flags().Lookup("allow-flagged") == nil {
		t.Fatal("cp has no explicit review choice")
	}
}
