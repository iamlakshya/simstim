package client

import (
	"os"
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
