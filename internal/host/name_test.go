package host

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNewIDUnusedIDAndNameChecks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if err := (&Manifest{Schema: 1, ID: "oldoldid", Name: "abcd2345"}).Save(); err != nil {
		t.Fatal(err)
	}
	// unusedID skips existing IDs and existing session names.
	seq := []string{"oldoldid", "abcd2345", "zzzz2345"}
	oldDraw := drawID
	i := 0
	drawID = func() string {
		v := seq[i]
		i++
		return v
	}
	t.Cleanup(func() { drawID = oldDraw })
	if got := unusedID(); got != "zzzz2345" {
		t.Fatalf("unusedID() = %q, want zzzz2345", got)
	}

	// checkName rejects ID-shaped names, overly long names, and existing IDs.
	for _, bad := range []string{"abcd2345", "oldoldid", strings.Repeat("x", api.SessionNameMax+1)} {
		var pe *proto.Error
		if err := checkName(bad); !errors.As(err, &pe) || pe.Code != "bad_name" {
			t.Errorf("checkName(%q) = %v, want bad_name", bad, err)
		}
	}
	for _, ok := range []string{"frontend", "abcd-2345", "auth-fix"} {
		if err := checkName(ok); err != nil {
			t.Errorf("checkName(%q) = %v", ok, err)
		}
	}
	// folderName appends -s for ID-shaped folders or folders matching an existing ID.
	for dir, want := range map[string]string{
		filepath.Join("/tmp", "proj2345"): "proj2345-s",
		filepath.Join("/tmp", "oldoldid"): "oldoldid-s",
		filepath.Join("/tmp", "frontend"): "frontend",
	} {
		if got := folderName(dir); got != want {
			t.Errorf("folderName(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestRenameAndDeleteNamedStoppedSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	oldSelf := SelfCgroup
	SelfCgroup = func() ([]byte, error) { return []byte("0::/user.slice/user-1000.slice/session-1.scope\n"), nil }
	t.Cleanup(func() { SelfCgroup = oldSelf })
	userManager(t, "cgroup:[1]", "cgroup:[1]", nil)

	id := "aaaa2345"
	if err := (&Manifest{Schema: 1, ID: id, Name: "first"}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := Rename(id, "second"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	m, err := LoadManifest(id)
	if err != nil || m.Name != "second" {
		t.Fatalf("manifest after Rename = %+v, %v", m, err)
	}
	// DeleteNamed with the old name refuses and preserves the session.
	var pe *proto.Error
	if _, err := DeleteNamed(id, "first"); !errors.As(err, &pe) || pe.Code != "session_renamed" {
		t.Fatalf("DeleteNamed with old name = %v, want session_renamed", err)
	}
	if _, err := LoadManifest(id); err != nil {
		t.Fatalf("session was deleted after session_renamed: %v", err)
	}
	// DeleteNamed with the current name removes it.
	res, err := DeleteNamed(id, "second")
	if err != nil || !res.Deleted {
		t.Fatalf("DeleteNamed with current name = %+v, %v", res, err)
	}
	if _, err := LoadManifest(id); err == nil {
		t.Fatal("session still exists after DeleteNamed")
	}
}
