package process

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type planFileInfo struct {
	name string
	mode fs.FileMode
}

func (f planFileInfo) Name() string       { return f.name }
func (f planFileInfo) Size() int64        { return 0 }
func (f planFileInfo) Mode() fs.FileMode  { return f.mode }
func (f planFileInfo) ModTime() time.Time { return time.Time{} }
func (f planFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f planFileInfo) Sys() any           { return nil }

func fakeFS(home string, dirs, files []string, links map[string]string) planFS {
	dirSet := map[string]bool{}
	for _, d := range dirs {
		dirSet[d] = true
	}
	fileSet := map[string]bool{}
	for _, f := range files {
		fileSet[f] = true
	}
	return planFS{
		lstat: func(p string) (fs.FileInfo, error) {
			if _, ok := links[p]; ok {
				return planFileInfo{name: filepath.Base(p), mode: os.ModeSymlink | 0o777}, nil
			}
			if dirSet[p] {
				return planFileInfo{name: filepath.Base(p), mode: os.ModeDir | 0o755}, nil
			}
			if fileSet[p] {
				return planFileInfo{name: filepath.Base(p), mode: 0o644}, nil
			}
			return nil, os.ErrNotExist
		},
		stat: func(p string) (fs.FileInfo, error) {
			if dirSet[p] {
				return planFileInfo{name: filepath.Base(p), mode: os.ModeDir | 0o755}, nil
			}
			if fileSet[p] {
				return planFileInfo{name: filepath.Base(p), mode: 0o644}, nil
			}
			if _, ok := links[p]; ok {
				return planFileInfo{name: filepath.Base(p), mode: 0o644}, nil
			}
			return nil, os.ErrNotExist
		},
		readlink: func(p string) (string, error) {
			if t, ok := links[p]; ok {
				return t, nil
			}
			return "", os.ErrNotExist
		},
		lookPath: func(name string) (string, error) {
			if name == "bwrap" {
				return "/usr/bin/bwrap", nil
			}
			return "", os.ErrNotExist
		},
		homeDir: func() (string, error) { return home, nil },
	}
}

func TestEtcAllowlistExcludesSensitiveHostFiles(t *testing.T) {
	list, err := EtcAllowlist()
	if err != nil {
		t.Fatalf("EtcAllowlist: %v", err)
	}
	for _, want := range []string{"ssl", "ld.so.cache", "nsswitch.conf", "localtime", "alternatives"} {
		if !slices.Contains(list, want) {
			t.Errorf("EtcAllowlist missing %q", want)
		}
	}
	for _, forbidden := range []string{"environment", "gitconfig", "shadow", "gshadow", "sudoers", "ssh", "passwd", "group", "hosts", "resolv.conf", "profile"} {
		if slices.Contains(list, forbidden) {
			t.Errorf("EtcAllowlist must not include %q", forbidden)
		}
	}
}

func TestBuildPlanArgsAndValidation(t *testing.T) {
	fsOps := fakeFS(
		"/home/dev",
		[]string{"/usr", "/etc/ssl", "/etc/alternatives"},
		[]string{"/etc/ld.so.cache", "/etc/environment", "/etc/shadow"},
		map[string]string{"/bin": "usr/bin", "/lib": "usr/lib", "/lib64": "usr/lib64"},
	)

	plan, err := buildPlanWithFS(Spec{
		SessionID:      "sess1234",
		HelperExe:      "/home/dev/.local/share/tool/bin/v1/helper",
		WorkspaceHost:  "/home/dev/src/app",
		WorkspaceGuest: "/work/app",
		StateHost:      "/home/dev/.local/state/tool/sessions/sess1234/state",
		StateDirs:      []string{".claude/projects"},
		ExtraRO:        []Mount{{Source: "/home/dev/.local/share/claude", Dest: "/opt/agents/install-0", ReadOnly: true}},
		DisableUserNS:  true,
	}, fsOps)
	if err != nil {
		t.Fatalf("buildPlanWithFS: %v", err)
	}

	joined := strings.Join(plan.Args, " ")
	for _, want := range []string{
		"--unshare-user", "--unshare-ipc", "--unshare-pid", "--unshare-uts", "--unshare-net",
		"--uid 1000 --gid 1000", "--cap-drop ALL", "--die-with-parent", "--new-session", "--disable-userns",
		"--proc /proc --dev /dev",
		"--ro-bind /usr /usr", "--symlink usr/bin /bin", "--symlink usr/lib /lib",
		"--ro-bind /etc/ssl /etc/ssl", "--ro-bind /etc/ld.so.cache /etc/ld.so.cache",
		"--ro-bind-data 5 /etc/passwd", "--ro-bind-data 6 /etc/group", "--ro-bind-data 7 /etc/hosts", "--ro-bind-data 8 /etc/resolv.conf",
		"--ro-bind /home/dev/.local/share/tool/bin/v1/helper /opt/",
		"--ro-bind /home/dev/.local/share/claude /opt/agents/install-0",
		"--tmpfs /tmp", "--tmpfs /var/tmp", "--perms 0700 --tmpfs /home/agent",
		"--bind /home/dev/.local/state/tool/sessions/sess1234/state/.claude/projects /home/agent/.claude/projects",
		"--bind /home/dev/src/app /work/app", "--chdir /work/app",
		"--clearenv", "--setenv HOME /home/agent", "--setenv USER agent",
		"--seccomp 4", "wall-init",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("bwrap args missing %q\nfull args: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"/etc/environment", "/etc/shadow"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("bwrap args leaked %q\nfull args: %s", forbidden, joined)
		}
	}
	if !slices.Contains(plan.Landlock.RODirs, "/") {
		t.Errorf("Landlock RODirs = %v, want /", plan.Landlock.RODirs)
	}
	for _, rw := range []string{"/work/app", "/home/agent", "/tmp", "/var/tmp", plan.GuestRunDir} {
		if !slices.Contains(plan.Landlock.RWDirs, rw) {
			t.Errorf("Landlock RWDirs = %v, missing %q", plan.Landlock.RWDirs, rw)
		}
	}

	// ShareNet omits --unshare-net.
	sharePlan, err := buildPlanWithFS(Spec{
		SessionID:      "sess1234",
		HelperExe:      "/usr/local/bin/helper",
		WorkspaceHost:  "/tmp/work",
		WorkspaceGuest: "/work/work",
		ShareNet:       true,
	}, fsOps)
	if err != nil {
		t.Fatalf("ShareNet plan: %v", err)
	}
	if slices.Contains(sharePlan.Args, "--unshare-net") {
		t.Errorf("ShareNet plan unexpectedly included --unshare-net")
	}

	// Validation rejections.
	badCases := []struct {
		name string
		spec Spec
	}{
		{
			name: "host-home-workspace",
			spec: Spec{SessionID: "s1", HelperExe: "/usr/bin/h", WorkspaceHost: "/home/dev", WorkspaceGuest: "/work/dev"},
		},
		{
			name: "root-workspace",
			spec: Spec{SessionID: "s1", HelperExe: "/usr/bin/h", WorkspaceHost: "/", WorkspaceGuest: "/work/root"},
		},
		{
			name: "state-traversal",
			spec: Spec{SessionID: "s1", HelperExe: "/usr/bin/h", WorkspaceHost: "/tmp/w", WorkspaceGuest: "/work/w", StateHost: "/tmp/st", StateDirs: []string{"../etc"}},
		},
		{
			name: "state-bashrc-refused",
			spec: Spec{SessionID: "s1", HelperExe: "/usr/bin/h", WorkspaceHost: "/tmp/w", WorkspaceGuest: "/work/w", StateHost: "/tmp/st", StateDirs: []string{".bashrc"}},
		},
		{
			name: "state-claude-settings-refused",
			spec: Spec{SessionID: "s1", HelperExe: "/usr/bin/h", WorkspaceHost: "/tmp/w", WorkspaceGuest: "/work/w", StateHost: "/tmp/st", StateDirs: []string{".claude/settings.json"}},
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildPlanWithFS(tc.spec, fsOps); err == nil {
				t.Fatalf("buildPlanWithFS(%s) succeeded, want error", tc.name)
			}
		})
	}
}

func TestWorkspaceGuestPath(t *testing.T) {
	if got := WorkspaceGuestPath("/tmp/my repo!"); got != "/work/my_repo_" {
		t.Fatalf("WorkspaceGuestPath = %q, want /work/my_repo_", got)
	}
	if got := WorkspaceGuestPath("/tmp/..."); got != "/work/workspace" {
		t.Fatalf("WorkspaceGuestPath dots = %q, want /work/workspace", got)
	}
}
