package guest

import (
	"bytes"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func nativeBundleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "/usr/bin/git", append(gitpolicy.Args(), append([]string{"-C", dir}, args...)...)...)
	command.Env = append(bundleEnvironment(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native fixture Git %s: %v %s", args[0], err, data)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func nativeBundleGuest(t *testing.T, changed, mixed bool) (*Init, *link.Session, proto.CtlBundleArgs) {
	t.Helper()
	if os.Getenv("GUEST_BUNDLE_GIT") != "1" || os.Geteuid() != AgentUID || os.Getegid() != AgentGID {
		t.Skip("opt-in native guest witness needs the actual unprivileged agent account")
	}
	if _, err := agentProcess(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "work", "repo")
	for _, dir := range []string{repo, filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	nativeBundleGit(t, repo, "init", "--template=", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeBundleGit(t, repo, "add", "base.txt")
	nativeBundleGit(t, repo, "commit", "-m", "seed")
	base := nativeBundleGit(t, repo, "rev-parse", "HEAD")
	prefix := brand.Name + "/primary2/"
	if mixed {
		nativeBundleGit(t, repo, "branch", prefix+"old", base)
	}
	nativeBundleGit(t, repo, "checkout", "-b", prefix+"topic")
	if changed {
		if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("committed work"), 0600); err != nil {
			t.Fatal(err)
		}
		nativeBundleGit(t, repo, "add", "work.txt")
		nativeBundleGit(t, repo, "commit", "-m", "work")
	}
	// Fixture construction ends before the guest receives its writable tree.
	// Every subsequent Git command on that tree uses the canonical runner.
	ours, theirs := net.Pipe()
	g := New(theirs, root, slog.New(slog.DiscardHandler))
	sup := link.New(ours, link.Supervisor, link.Options{})
	go g.Serve()
	t.Cleanup(func() {
		_ = sup.Close()
		select {
		case <-g.Done():
		case <-time.After(5 * time.Second):
			t.Error("the actual guest bundle owner did not stop")
		}
	})
	id, err := bundleNonce()
	if err != nil {
		t.Fatal(err)
	}
	return g, sup, proto.CtlBundleArgs{ID: id, Primary: "primary2", Cwd: "/work/repo", SeedHeads: []string{base}}
}

func readNativeGuestBundle(t *testing.T, sup *link.Session, id string) []byte {
	t.Helper()
	stream, err := sup.Open(t.Context(), link.KindBundle, proto.ExecHeader{ID: id, Purpose: "pull"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err = link.NewExecRecords(stream).CloseInput(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	diagnostic := &bundleText{remaining: 64 << 10}
	code, err := link.ReadExecOutput(stream, &out, diagnostic)
	if err != nil || code != 0 {
		t.Fatal("actual owned native bundle failed", code, err, diagnostic.data.String())
	}
	return out.Bytes()
}

func TestNativeGuestMixedOldAndNewRefsPreserveCompleteFullSnapshot(t *testing.T) {
	g, sup, args := nativeBundleGuest(t, true, true)
	ready, err := g.prepareBundle(args)
	if err != nil || ready.Empty || !ready.Full {
		t.Fatal("mixed excluded-tip capture did not choose honest full mode", ready, err)
	}
	data := readNativeGuestBundle(t, sup, args.ID)
	header, err := gitpolicy.ReadBundleHeader(bytes.NewReader(data), 1<<20)
	if err != nil || len(header.Prerequisites) != 0 || len(header.Heads) != 2 {
		t.Fatal("native full fallback omitted an unchanged captured ref", header, err)
	}
	verifyNativeBundle(t, data)
	g.cancelBundle(args.ID)
}

func verifyNativeBundle(t *testing.T, data []byte) {
	t.Helper()
	local := t.TempDir()
	nativeBundleGit(t, local, "init", "--bare", "--template=")
	path := filepath.Join(t.TempDir(), "work.bundle")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	nativeBundleGit(t, local, "bundle", "verify", path)
}

func TestNativeGuestUnchangedCaptureRegistersNoBundleCommand(t *testing.T) {
	g, _, args := nativeBundleGuest(t, false, false)
	ready, err := g.prepareBundle(args)
	if err != nil || !ready.Empty {
		t.Fatal(ready, err)
	}
	g.mu.Lock()
	run := g.execs[args.ID]
	g.mu.Unlock()
	if run != nil {
		t.Fatal("no-new-commit capture registered a final bundle command")
	}
	g.cancelBundle(args.ID)
}

func TestNativeGuestMissingSeedRetryUsesSameCompleteSnapshot(t *testing.T) {
	g, sup, args := nativeBundleGuest(t, true, false)
	ready, err := g.prepareBundle(args)
	if err != nil || ready.Empty || ready.Full {
		t.Fatal("all-new positive control did not stay incremental", ready, err)
	}
	partial := readNativeGuestBundle(t, sup, args.ID)
	header, err := gitpolicy.ReadBundleHeader(bytes.NewReader(partial), 1<<20)
	if err != nil || len(header.Prerequisites) == 0 {
		t.Fatal("native incremental positive control lacks its seed prerequisite", err)
	}
	var heads []proto.BundleRef
	for name, oid := range header.Heads {
		heads = append(heads, proto.BundleRef{Name: name, OID: oid})
	}
	args.Full, args.DefaultRef = true, ready.DefaultRef
	args.HeadCount, args.HeadsDigest = proto.BundleSnapshot(heads)
	ready, err = g.prepareBundle(args)
	if err != nil || !ready.Full {
		t.Fatal("same owned native full retry failed", ready, err)
	}
	full := readNativeGuestBundle(t, sup, args.ID)
	whole, err := gitpolicy.ReadBundleHeader(bytes.NewReader(full), 1<<20)
	if err != nil || len(whole.Prerequisites) != 0 || !reflect.DeepEqual(header.Heads, whole.Heads) {
		t.Fatal("full native retry changed the captured namespace", whole, err)
	}
	verifyNativeBundle(t, full)
	g.cancelBundle(args.ID)
}
