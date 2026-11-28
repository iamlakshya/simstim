package client

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRemotePath(t *testing.T) {
	h, _ := os.UserHomeDir()
	for in, want := range map[string]string{h: "~", h + "/src/app": "~/src/app", "/srv/app": "/srv/app", "~/x": "~/x", h + "other/x": h + "other/x"} {
		if got := RemotePath(in); got != want {
			t.Errorf("RemotePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNeedMakeFolder(t *testing.T) {
	c := &Conn{Host: Host{Name: "old"}}
	if err := c.needMakeFolder(NewArgs{}); err != nil {
		t.Fatal(err)
	}
	if err := c.needMakeFolder(NewArgs{MakeFolder: true}); err == nil {
		t.Fatal("an old helper got a folder to make, which it would ignore")
	}
	c.Features = []string{"mkdir"}
	if err := c.needMakeFolder(NewArgs{MakeFolder: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCapProbeRequiresSandboxFeature(t *testing.T) {
	c := &Conn{Host: Host{Name: "old"}}
	_, err := c.CapProbe(context.Background(), "")
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "wall_helper_old" || !strings.Contains(ue.Msg, "too old for sandboxes") || ue.Hint == "" {
		t.Fatalf("CapProbe without sandbox feature = %v, want wall_helper_old with hint", err)
	}
}

// A name of the form of an ID, or too long, fails before any request, with
// bad_name and exit code 2, as the helper refuses it too (cli-spec 9.2).
func TestNamesThatNoRequestCarries(t *testing.T) {
	c := &Conn{Host: Host{Name: "box"}}
	for _, name := range []string{"abcd2345", " abcd2345", strings.Repeat("n", 65)} {
		var ue *UserError
		if _, err := c.NewSession(context.Background(), NewArgs{Name: name}); !errors.As(err, &ue) || ue.Code != "bad_name" || ue.Kind != KindUsage {
			t.Errorf("new %.10q: %v", name, err)
		}
		if err := c.RenameSession(context.Background(), "abcd2345", name); !errors.As(err, &ue) || ue.Code != "bad_name" || ue.Hint == "" {
			t.Errorf("rename to %.10q: %v", name, err)
		}
	}
	if err := CheckSessionName("abcd-2345"); err != nil {
		t.Errorf("a name with a dash: %v", err)
	}
}

func TestEmptyRenameStopsBeforeRequest(t *testing.T) {
	c := &Conn{Host: Host{Name: "box"}, err: errors.New("unexpected request")}
	for _, name := range []string{"", " ", "\t\n"} {
		var ue *UserError
		if err := c.RenameSession(context.Background(), "abcd2345", name); !errors.As(err, &ue) || ue.Code != "bad_name" || ue.Kind != KindUsage || ue.Hint == "" {
			t.Errorf("empty rename reached request path: %v", err)
		}
	}
}
