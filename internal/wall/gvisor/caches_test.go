package gvisor

import (
	"slices"
	"testing"
)

func TestBuildBundlePlanCacheMountsAndRefusals(t *testing.T) {
	fsOps := bundleFS{
		homeDir:  func() (string, error) { return "/home/dev", nil },
		getuid:   func() int { return 1000 },
		getgid:   func() int { return 1000 },
		stat:     defaultBundleFS.stat,
		lstat:    defaultBundleFS.lstat,
		readlink: defaultBundleFS.readlink,
	}

	plan, err := buildBundlePlanWithFS("/tmp/bundle", Spec{
		SessionID:      "gvcache1",
		SessionDir:     "/tmp/sess",
		HelperExe:      "/usr/local/bin/helper",
		WorkspaceHost:  "/tmp/work",
		WorkspaceGuest: "/work/app",
		StateHost:      "/tmp/sess/state",
		StateDirs:      []string{".claude/projects"},
		CacheHost:      "/tmp/sess/caches",
		CacheDirs:      []string{".npm/_cacache", ".local/share/pnpm/store", ".claude/projects"},
	}, fsOps)
	if err != nil {
		t.Fatalf("buildBundlePlanWithFS: %v", err)
	}
	if len(plan.CachePaths) != 2 {
		t.Fatalf("CachePaths = %v, want 2 entries (StateDir deduped)", plan.CachePaths)
	}
	var sawNPM, sawPNPM bool
	for _, m := range plan.Config.Mounts {
		if m.Destination == "/home/agent/.npm/_cacache" && m.Source == "/tmp/sess/caches/.npm/_cacache" && slices.Contains(m.Options, "rw") {
			sawNPM = true
		}
		if m.Destination == "/home/agent/.local/share/pnpm/store" && m.Source == "/tmp/sess/caches/.local/share/pnpm/store" && slices.Contains(m.Options, "rw") {
			sawPNPM = true
		}
	}
	if !sawNPM || !sawPNPM {
		t.Fatalf("missing expected cache mounts (npm=%v pnpm=%v): %+v", sawNPM, sawPNPM, plan.Config.Mounts)
	}

	for _, bad := range []Spec{
		{SessionID: "gv1", HelperExe: "/usr/bin/h", WorkspaceHost: "/tmp/w", WorkspaceGuest: "/work/w", CacheHost: "/tmp/c", CacheDirs: []string{".cargo/bin"}},
		{SessionID: "gv1", HelperExe: "/usr/bin/h", WorkspaceHost: "/tmp/w", WorkspaceGuest: "/work/w", CacheHost: "/tmp/c", CacheDirs: []string{"../escape"}},
		{SessionID: "gv1", HelperExe: "/usr/bin/h", WorkspaceHost: "/tmp/w", WorkspaceGuest: "/work/w", CacheDirs: []string{".npm/_cacache"}},
	} {
		if _, err := buildBundlePlanWithFS("/tmp/bundle", bad, fsOps); err == nil {
			t.Errorf("buildBundlePlanWithFS(%+v) succeeded, want error", bad.CacheDirs)
		}
	}
}
