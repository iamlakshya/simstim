package jail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func bootstrapFixtureV1(t *testing.T, name, data string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestBootstrapClosureChangesWithCOnlyBytesOrMode(t *testing.T) {
	ctx := context.Background()
	c := bootstrapFixtureV1(t, "launcher", "fixed C bytes")
	helper := bootstrapFixtureV1(t, "helper", "fixed Go bytes")
	before, err := gvisorCodeClosureV1(ctx, c, helper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Name(), []byte("other C bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	after, err := gvisorCodeClosureV1(ctx, c, helper)
	if err != nil || after == before {
		t.Fatal("C-only bytes reused an old key", err)
	}
	if err := os.Chmod(c.Name(), 0700); err != nil {
		t.Fatal(err)
	}
	mode, err := gvisorCodeClosureV1(ctx, c, helper)
	if err != nil || mode == after {
		t.Fatal("C-only mode reused an old key", err)
	}
}

func TestHeldBootstrapCannotFollowPathReplacementOrAcceptInPlaceCAS(t *testing.T) {
	ctx := context.Background()
	c := bootstrapFixtureV1(t, "launcher", "original C bytes")
	helper := bootstrapFixtureV1(t, "helper", "fixed Go bytes")
	original, err := gvisorCodeClosureV1(ctx, c, helper)
	if err != nil {
		t.Fatal(err)
	}
	source := &GvisorBootstrapV1{launcher: c, helper: helper, identity: original}
	// Atomic installation leaves the held original executable selected. The
	// replacement is a different capture and cannot borrow the original key.
	replacement := c.Name() + ".new"
	if err := os.WriteFile(replacement, []byte("replacement C bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, c.Name()); err != nil {
		t.Fatal(err)
	}
	// Rename can change ctime on the old inode. Capture the now-stable original
	// identity, then show retention neither opens nor executes the replacement.
	source.identity, err = gvisorCodeClosureV1(ctx, c, helper)
	if err != nil {
		t.Fatal(err)
	}
	launcher, heldHelper, key, err := source.retain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer launcher.Close()
	defer heldHelper.Close()
	if key != source.identity {
		t.Fatal("retention changed original code identity")
	}
	current, err := os.Open(c.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	changed, err := gvisorCodeClosureV1(ctx, current, helper)
	if err != nil || changed == key {
		t.Fatal("replacement C reused old held key", err)
	}
	// Mutating the actual held inode after lookup must refuse before creation
	// and at the physical producer's publication bracket.
	if err := os.WriteFile(helper.Name(), []byte("other Go bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := source.IdentityV1(ctx); err == nil {
		t.Fatal("in-place code CAS passed original identity")
	}
	if _, _, _, err := source.retain(ctx); err == nil {
		t.Fatal("changed source issued another command capture")
	}
}

func TestZeroBootstrapNeverIssuesCode(t *testing.T) {
	var source GvisorBootstrapV1
	if _, err := source.IdentityV1(context.Background()); err == nil {
		t.Fatal("zero source issued code")
	}
	if _, _, _, err := source.retain(context.Background()); err == nil {
		t.Fatal("zero source retained code")
	}
}
