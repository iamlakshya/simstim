package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestBundlePullQuarantinesTagsAndStopsBeforeFlaggedBranchMove(t *testing.T) {
	fixture := makeNativePull(t, ".github/workflows/ci.yml", "name: changed\n", true)
	peer := nativePullPeer(fixture)
	beforeHead := pullFixtureGit(t, fixture.local, "rev-parse", "HEAD")
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target"})
	var flagged *PullReviewError
	if !errors.As(err, &flagged) || flagged.Failure.Code != "pull_flagged" || result == nil || result.Moved {
		t.Fatalf("flagged branch moved: %+v %v", result, err)
	}
	refs := localRefs(t, fixture.local)
	if strings.Contains(refs, "refs/heads/review-target") || strings.Contains(refs, "refs/tags/bait") || strings.Contains(refs, "refs/heads/foreign") {
		t.Fatalf("outside refs changed: %s", refs)
	}
	if !strings.Contains(refs, "refs/"+brand.Name+"/sessions/") {
		t.Fatal("verified work was not retained in quarantine")
	}
	if got := pullFixtureGit(t, fixture.local, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatal("HEAD changed")
	}
	if _, err := os.Stat(filepath.Join(fixture.local, ".github")); !os.IsNotExist(err) {
		t.Fatal("the laptop checked out guest files")
	}
	result, err = pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", AllowFlagged: true})
	if err != nil || !result.Moved || !result.Acknowledged {
		t.Fatalf("explicit review did not move branch: %+v %v", result, err)
	}
	if got := pullFixtureGit(t, fixture.local, "rev-parse", "refs/heads/review-target"); got != fixture.tip {
		t.Fatal(got)
	}
	refs = localRefs(t, fixture.local)
	if strings.Contains(refs, "refs/tags/bait") || strings.Contains(refs, "refs/heads/foreign") {
		t.Fatal("tag or foreign branch escaped quarantine")
	}
}

func TestMissingSeedHistoryRequestsOneFullBundle(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "new work\n", false)
	unrelated := t.TempDir()
	pullFixtureGit(t, unrelated, "init", "--template=", "--initial-branch=main")
	pullFixtureCommit(t, unrelated, "local.txt", "unrelated")
	peer := nativePullPeer(fixture)
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: unrelated, Into: "review-target"})
	if err != nil || !result.Full || !result.Moved {
		t.Fatalf("full fallback failed: %+v %v", result, err)
	}
	if len(peer.begins) != 2 || peer.begins[0].Full || !peer.begins[1].Full || peer.begins[1].Previous != peer.begins[0].Token {
		t.Fatal("full retry lost exact prior ownership")
	}
}

func TestEmptyBundlePullChangesNoRef(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "work", false)
	before := localRefs(t, fixture.local)
	peer := &pullPeerFixture{supported: true}
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local})
	if err != nil || !result.Empty || result.Moved {
		t.Fatalf("%+v %v", result, err)
	}
	if after := localRefs(t, fixture.local); after != before {
		t.Fatal("no-new-commit pull changed a ref")
	}
}

func TestReviewedBranchCASPreservesConcurrentMove(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "work", false)
	repository, err := openPullGit(t.Context(), fixture.local)
	if err != nil {
		t.Fatal(err)
	}
	target, old, err := repository.branchState(t.Context(), "review-target")
	if err != nil {
		t.Fatal(err)
	}
	pullFixtureGit(t, fixture.local, "update-ref", target, fixture.base)
	if err = repository.moveBranch(t.Context(), target, old, fixture.base, true); err == nil {
		t.Fatal("review overwrote a concurrent branch change")
	}
	if got := pullFixtureGit(t, fixture.local, "rev-parse", target); got != fixture.base {
		t.Fatal(got)
	}
	if err = repository.moveBranch(t.Context(), "refs/heads/main", fixture.base, fixture.base, true); err == nil {
		t.Fatal("force bypassed checked-out branch refusal")
	}
}

func TestModuleCRRefusalCannotBeOverridden(t *testing.T) {
	fixture := makeNativePull(t, ".gitmodules", "[submodule \"fixture\"]\n path = \"path\rhook\"\n url = https://example.invalid/repo\n", false)
	peer := nativePullPeer(fixture)
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", AllowFlagged: true})
	var refused *UserError
	if !errors.As(err, &refused) || refused.Kind != KindRefused || result != nil && result.Moved {
		t.Fatalf("modules CR was not a hard refusal: %+v %v", result, err)
	}
	if strings.Contains(localRefs(t, fixture.local), "refs/heads/review-target") {
		t.Fatal("unsafe modules changed target")
	}
}

func TestNativeModuleCRLFIsNotMistakenForDecodedValueCR(t *testing.T) {
	fixture := makeNativePull(t, ".gitmodules", "[submodule \"fixture\"]\r\n path = subdir\r\n url = https://example.invalid/repo\r\n", false)
	result, err := pullSandbox(t.Context(), nativePullPeer(fixture), "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", AllowFlagged: true})
	if err != nil || result == nil || !result.Moved || !result.ReviewComplete {
		t.Fatal("native clean CRLF values were hard refused", result, err)
	}
	for _, finding := range result.Findings {
		if finding.Refused {
			t.Fatal("line endings became decoded value CR", finding)
		}
	}
}

func TestAckFailureReportsAlreadyMovedBranchAndKeepsQuarantine(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "work", false)
	peer := nativePullPeer(fixture)
	peer.ackError = errors.New("fixture ack failure")
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target"})
	var failure *UserError
	if !errors.As(err, &failure) || failure.Code != "pull_ack" || result == nil || !result.Moved || result.Acknowledged {
		t.Fatal("ack failure hid the actual local outcome", result, err)
	}
	if got := pullFixtureGit(t, fixture.local, "rev-parse", "refs/heads/review-target"); got != fixture.tip || len(result.Quarantine) == 0 {
		t.Fatal("ack failure lost the moved branch or quarantine")
	}
}

func TestPullAcknowledgesOnlyTheSelectedReviewedTip(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "reviewed work", false)
	other := pullNamespace("primary2") + "unreviewed"
	pullFixtureGit(t, fixture.source, "checkout", "-b", strings.TrimPrefix(other, "refs/heads/"))
	pullFixtureCommit(t, fixture.source, ".github/workflows/other.yml", "name: unreviewed\n")
	unreviewed := pullFixtureGit(t, fixture.source, "rev-parse", "HEAD")
	pullFixtureGit(t, fixture.source, "checkout", strings.TrimPrefix(fixture.ref, "refs/heads/"))
	for _, whole := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "snapshot.bundle")
		args := []string{"bundle", "create", path, fixture.ref, other}
		if !whole {
			args = append(args, "--not", fixture.base)
		}
		pullFixtureGit(t, fixture.source, args...)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if whole {
			fixture.full = data
		} else {
			fixture.partial = data
		}
	}
	peer := nativePullPeer(fixture)
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target"})
	if err != nil || result == nil || !result.Moved || len(result.Quarantine) != 2 {
		t.Fatal("selected-only review positive control failed", result, err)
	}
	acknowledged := 0
	for _, ack := range peer.acks {
		if ack.Abort {
			continue
		}
		if len(ack.Commits) != 1 || ack.Commits[0] != fixture.tip || ack.Commits[0] == unreviewed {
			t.Fatal("an unreviewed quarantined branch was acknowledged", ack)
		}
		acknowledged++
	}
	if acknowledged != 1 {
		t.Fatal("the reviewed selected tip was not acknowledged once")
	}
}

func TestPullRefusesTargetCheckedOutInAnotherWorktreeEvenWithForce(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "work", false)
	other := filepath.Join(t.TempDir(), "other worktree")
	pullFixtureGit(t, fixture.local, "worktree", "add", "-b", "review-target", other, fixture.base)
	peer := nativePullPeer(fixture)
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", Force: true, AllowFlagged: true})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "branch_checked_out" || result == nil || result.Moved {
		t.Fatal("force moved a branch checked out in another worktree", result, err)
	}
	if got := pullFixtureGit(t, other, "rev-parse", "HEAD"); got != fixture.base {
		t.Fatal("another worktree's HEAD changed")
	}
}
