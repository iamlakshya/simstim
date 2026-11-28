package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func initTestGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME":     "test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
	} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "a.txt")
	runGit("commit", "-q", "-m", "initial")
	return runGit("rev-parse", "HEAD")
}

func addTestGitCommit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", file}, {"commit", "-q", "-m", msg}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestUnpulledCommitsAndFinishSession(t *testing.T) {
	alertHome(t)
	id := "abcd2345"
	repoDir := filepath.Join(paths.WorkDir(id), "app")
	baseSHA := initTestGitRepo(t, repoDir)

	m := &Manifest{
		Schema:    1,
		ID:        id,
		Name:      "auth-fix",
		CreatedAt: time.Now().UTC(),
		Cwd:       repoDir,
		Agent:     AgentInfo{Kind: "shell"},
		Status:    Status{State: StateExited, Since: time.Now().UTC()},
		SeedRepos: []api.SeedRepoRecord{
			{Name: "app", Dir: repoDir, Branch: "main", SeedHead: baseSHA},
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if got := UnpulledCommits(m); got != 0 {
		t.Fatalf("UnpulledCommits at seed base = %d, want 0", got)
	}

	addTestGitCommit(t, repoDir, "b.txt", "c1\n", "commit 1")
	head2 := addTestGitCommit(t, repoDir, "c.txt", "c2\n", "commit 2")
	if got := UnpulledCommits(m); got != 2 {
		t.Fatalf("UnpulledCommits after 2 commits = %d, want 2", got)
	}

	fin, err := FinishSession(id)
	if err != nil {
		t.Fatalf("FinishSession with unpulled commits: %v", err)
	}
	wantMsg := "auth-fix has 2 commits that you have not pulled, so it stopped instead of being removed"
	if fin.Action != "stopped" || fin.UnpulledCommits != 2 || fin.Message != wantMsg {
		t.Fatalf("FinishSession = %+v, want stopped/2/%q", fin, wantMsg)
	}
	after, err := LoadManifest(id)
	if err != nil {
		t.Fatalf("session was removed despite unpulled commits: %v", err)
	}
	if after.Status.State != StateStopped {
		t.Fatalf("state after FinishSession = %q, want %q", after.Status.State, StateStopped)
	}

	if err := RecordPulled(api.PulledArgs{ID: id, Commits: []string{head2}}); err != nil {
		t.Fatalf("RecordPulled: %v", err)
	}
	after, err = LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := UnpulledCommits(after); got != 0 {
		t.Fatalf("UnpulledCommits after RecordPulled = %d, want 0", got)
	}

	fin, err = FinishSession(id)
	if err != nil {
		t.Fatalf("FinishSession after pull: %v", err)
	}
	if fin.Action != "removed" {
		t.Fatalf("FinishSession after pull = %+v, want removed", fin)
	}
	if _, err := LoadManifest(id); err == nil {
		t.Fatal("session manifest still exists after removal")
	}
}

func TestPruneSessionsDryRunStoppedBeforeAndUnpulled(t *testing.T) {
	alertHome(t)
	now := time.Now().UTC()

	saveStopped := func(id, name string, stoppedAt time.Time, dirty bool) {
		t.Helper()
		repoDir := filepath.Join(paths.WorkDir(id), "repo")
		baseSHA := initTestGitRepo(t, repoDir)
		if dirty {
			addTestGitCommit(t, repoDir, "dirty.txt", "change\n", "unpulled")
		}
		m := &Manifest{
			Schema:    1,
			ID:        id,
			Name:      name,
			CreatedAt: stoppedAt.Add(-time.Hour),
			Cwd:       repoDir,
			Agent:     AgentInfo{Kind: "shell"},
			Status:    Status{State: StateStopped, Since: stoppedAt},
			SeedRepos: []api.SeedRepoRecord{{Name: "repo", Dir: repoDir, Branch: "main", SeedHead: baseSHA}},
		}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
	}

	saveStopped("old23456", "old-clean", now.Add(-10*24*time.Hour), false)
	saveStopped("new23456", "new-clean", now.Add(-1*time.Hour), false)
	saveStopped("drt23456", "old-dirty", now.Add(-9*24*time.Hour), true)

	cutoff := now.Add(-7 * 24 * time.Hour)
	dry, err := PruneSessions(api.PruneArgs{StoppedBefore: &cutoff, DryRun: true})
	if err != nil {
		t.Fatalf("PruneSessions dry-run: %v", err)
	}
	if len(dry.Sessions) != 2 {
		t.Fatalf("dry-run sessions = %+v, want 2 (old-clean, old-dirty)", dry.Sessions)
	}
	if dry.Sessions[0].ID != "old23456" || dry.Sessions[0].Action != "would_remove" {
		t.Fatalf("dry-run[0] = %+v, want old23456 would_remove", dry.Sessions[0])
	}
	if dry.Sessions[1].ID != "drt23456" || dry.Sessions[1].Action != "kept" || dry.Sessions[1].UnpulledCommits != 1 {
		t.Fatalf("dry-run[1] = %+v, want drt23456 kept (1 unpulled)", dry.Sessions[1])
	}
	if _, err := LoadManifest("old23456"); err != nil {
		t.Fatal("dry-run removed old-clean")
	}

	res, err := PruneSessions(api.PruneArgs{StoppedBefore: &cutoff, DryRun: false})
	if err != nil {
		t.Fatalf("PruneSessions apply: %v", err)
	}
	if len(res.Sessions) != 2 || res.Sessions[0].Action != "removed" || res.Sessions[1].Action != "kept" {
		t.Fatalf("apply sessions = %+v", res.Sessions)
	}
	if _, err := LoadManifest("old23456"); err == nil {
		t.Fatal("old-clean still exists after prune")
	}
	if _, err := LoadManifest("drt23456"); err != nil {
		t.Fatal("old-dirty was removed despite unpulled commit")
	}
	if _, err := LoadManifest("new23456"); err != nil {
		t.Fatal("new-clean was removed despite --stopped-before")
	}
}

func TestPruneSessionsSkipsSandboxChildren(t *testing.T) {
	alertHome(t)
	now := time.Now().UTC()

	savePrimaryAndChild := func(primaryID, childID, name string, dirty bool) {
		t.Helper()
		repoDir := filepath.Join(paths.WorkDir(primaryID), "repo")
		baseSHA := initTestGitRepo(t, repoDir)
		if dirty {
			addTestGitCommit(t, repoDir, "unpulled.txt", "work\n", "unpulled commit")
		}
		pm := &Manifest{
			Schema:    1,
			ID:        primaryID,
			Name:      name,
			CreatedAt: now.Add(-2 * time.Hour),
			Cwd:       repoDir,
			Agent:     AgentInfo{Kind: "claude"},
			Status:    Status{State: StateStopped, Since: now.Add(-time.Hour)},
			Wall:      &WallRecord{Level: "process", Driver: "process"},
			SeedRepos: []api.SeedRepoRecord{{Name: "repo", Dir: repoDir, Branch: "main", SeedHead: baseSHA}},
		}
		cm := &Manifest{
			Schema:    1,
			ID:        childID,
			Name:      name + "-shell",
			SandboxOf: primaryID,
			CreatedAt: now.Add(-2 * time.Hour),
			Cwd:       repoDir,
			Agent:     AgentInfo{Kind: "shell"},
			Status:    Status{State: StateStopped, Since: now.Add(-time.Hour)},
			Wall:      &WallRecord{Level: "process", Driver: "process"},
		}
		if err := pm.Save(); err != nil {
			t.Fatal(err)
		}
		if err := cm.Save(); err != nil {
			t.Fatal(err)
		}
	}

	// Clean primary + child and dirty primary + child.
	savePrimaryAndChild("prm33333", "chd33333", "clean-box", false)
	savePrimaryAndChild("prm22222", "chd22222", "dirty-box", true)

	// Dry-run lists only the two primary sandboxes (never double-counting children).
	dry, err := PruneSessions(api.PruneArgs{DryRun: true})
	if err != nil {
		t.Fatalf("PruneSessions dry-run: %v", err)
	}
	if len(dry.Sessions) != 2 {
		t.Fatalf("dry-run sessions = %+v, want 2 primary sandboxes only", dry.Sessions)
	}
	for _, row := range dry.Sessions {
		if row.ID == "chd33333" || row.ID == "chd22222" {
			t.Fatalf("dry-run unexpectedly listed child session %+v", row)
		}
	}

	// Real prune deletes clean primary + its child (no error rows) and keeps dirty primary + its child.
	res, err := PruneSessions(api.PruneArgs{DryRun: false})
	if err != nil {
		t.Fatalf("PruneSessions apply: %v", err)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("apply sessions = %+v, want 2 rows", res.Sessions)
	}
	byID := map[string]api.PruneSession{}
	for _, row := range res.Sessions {
		byID[row.ID] = row
		if row.Action == "error" {
			t.Fatalf("unexpected prune error row: %+v", row)
		}
	}
	if byID["prm33333"].Action != "removed" || byID["prm22222"].Action != "kept" {
		t.Fatalf("unexpected prune actions: %+v", byID)
	}
	if _, err := LoadManifest("prm33333"); err == nil {
		t.Fatal("clean primary still exists after prune")
	}
	if _, err := LoadManifest("chd33333"); err == nil {
		t.Fatal("clean child was not removed with its primary")
	}
	if _, err := LoadManifest("prm22222"); err != nil {
		t.Fatalf("dirty primary was removed despite unpulled commit: %v", err)
	}
	if _, err := LoadManifest("chd22222"); err != nil {
		t.Fatalf("child of kept dirty primary was silently deleted: %v", err)
	}
}

func openBridgeEventsStream(t *testing.T, args api.EventsWatchArgs) (*link.Stream, func()) {
	t.Helper()
	cliConn, srvConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunBridge(srvConn, srvConn)
		_ = srvConn.Close()
	}()

	_ = cliConn.SetDeadline(time.Now().Add(5 * time.Second))
	bw := proto.NewWriter(cliConn)
	br := bufio.NewReaderSize(cliConn, 64<<10)
	if err := bw.WriteJSON(proto.THello, proto.Hello{
		Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client",
	}); err != nil {
		t.Fatal(err)
	}
	hf, err := proto.Read(br)
	if err != nil || hf.Type != proto.THello {
		t.Fatalf("read hello: %v %+v", err, hf)
	}
	var srvHello proto.Hello
	if err := json.Unmarshal(hf.Payload, &srvHello); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(srvHello.Features, api.FeatureEvents) {
		t.Fatalf("bridge hello features %v lack %q", srvHello.Features, api.FeatureEvents)
	}

	rawArgs, _ := json.Marshal(args)
	if err := bw.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "events.watch", Args: rawArgs}); err != nil {
		t.Fatal(err)
	}
	rf, err := proto.Read(br)
	if err != nil || rf.Type != proto.TResponse {
		t.Fatalf("read response: %v %+v", err, rf)
	}
	var resp proto.Response
	if err := json.Unmarshal(rf.Payload, &resp); err != nil || !resp.OK {
		t.Fatalf("events.watch response: %+v err=%v", resp, err)
	}
	_ = cliConn.SetDeadline(time.Time{})

	lnk := link.New(&bufferedNetConn{Conn: cliConn, br: br}, link.Guest, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
		MaxSupervisorStreams: 4,
		MaxGuestStreams:      1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := lnk.Accept(ctx)
	cancel()
	if err != nil {
		t.Fatalf("accept events stream: %v", err)
	}
	if st.Kind() != link.KindEvents {
		t.Fatalf("stream kind = %q, want %q", st.Kind(), link.KindEvents)
	}
	cleanup := func() {
		_ = st.Close()
		_ = lnk.Close()
		_ = cliConn.Close()
		<-done
	}
	return st, cleanup
}

type bufferedNetConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedNetConn) Read(p []byte) (int, error) { return c.br.Read(p) }

func TestBridgeEventsWatchHistoryAndLiveLatency(t *testing.T) {
	alertHome(t)

	RecordWatchEvent(api.WatchEvent{Event: api.EventSandboxReady, Session: "sess2345", Name: "alpha"})
	RecordWatchEvent(api.WatchEvent{Event: api.EventNeedsInput, Session: "othr2345", Name: "beta", Detail: "Approve?"})

	// One-shot history filtered by session (with hard read timeout per M0184).
	st, cleanup := openBridgeEventsStream(t, api.EventsWatchArgs{Session: "sess2345", Follow: false})
	type readRes struct {
		data []byte
		err  error
	}
	resCh := make(chan readRes, 1)
	go func() {
		b, err := io.ReadAll(st)
		resCh <- readRes{data: b, err: err}
	}()
	var data []byte
	select {
	case r := <-resCh:
		if r.err != nil {
			cleanup()
			t.Fatal(r.err)
		}
		data = r.data
	case <-time.After(5 * time.Second):
		cleanup()
		t.Fatal("timed out reading events.watch history stream")
	}
	cleanup()
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("history filtered lines = %d (%q), want 1", len(lines), string(data))
	}
	var got api.WatchEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil || got.Event != api.EventSandboxReady || got.Session != "sess2345" {
		t.Fatalf("decoded history event = %+v err=%v", got, err)
	}

	// Live --follow latency check: spec 12 requires <= 200 ms at p50.
	st, cleanup = openBridgeEventsStream(t, api.EventsWatchArgs{Session: "sess2345", Follow: true})
	defer cleanup()

	// Give the bridge watcher a brief moment to register its inotify watch.
	time.Sleep(30 * time.Millisecond)

	const samples = 11
	lineCh := make(chan []byte, samples*2)
	scanErrCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(st)
		for sc.Scan() {
			lineCh <- append([]byte(nil), sc.Bytes()...)
		}
		scanErrCh <- sc.Err()
	}()
	latencies := make([]time.Duration, 0, samples)
	for i := range samples {
		sentAt := time.Now()
		RecordWatchEvent(api.WatchEvent{
			Event:   api.EventNeedsInput,
			Session: "sess2345",
			Name:    "alpha",
			Detail:  "Question",
		})
		select {
		case raw := <-lineCh:
			latencies = append(latencies, time.Since(sentAt))
			var ev api.WatchEvent
			if err := json.Unmarshal(raw, &ev); err != nil || ev.Event != api.EventNeedsInput {
				t.Fatalf("sample %d: event = %+v err=%v", i, ev, err)
			}
		case err := <-scanErrCh:
			t.Fatalf("sample %d: scanner ended: %v", i, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("sample %d: timed out waiting for live event", i)
		}
		time.Sleep(10 * time.Millisecond)
	}
	slices.Sort(latencies)
	p50 := latencies[len(latencies)/2]
	if p50 > 200*time.Millisecond {
		t.Fatalf("events.watch p50 latency = %v, want <= 200ms (all: %v)", p50, latencies)
	}
}

func TestBridgeEventsWatchRefusedInsideSession(t *testing.T) {
	alertHome(t)
	RecordWatchEvent(api.WatchEvent{Event: api.EventNeedsInput, Session: "secr2345", Name: "other", Detail: "secret prompt"})

	checkRefused := func(t *testing.T) {
		t.Helper()
		cliConn, srvConn := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = RunBridge(srvConn, srvConn)
			_ = srvConn.Close()
		}()
		defer func() {
			_ = cliConn.Close()
			<-done
		}()

		_ = cliConn.SetDeadline(time.Now().Add(5 * time.Second))
		bw := proto.NewWriter(cliConn)
		br := bufio.NewReaderSize(cliConn, 64<<10)
		if err := bw.WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client",
		}); err != nil {
			t.Fatal(err)
		}
		if hf, err := proto.Read(br); err != nil || hf.Type != proto.THello {
			t.Fatalf("read hello: %v %+v", err, hf)
		}
		rawArgs, _ := json.Marshal(api.EventsWatchArgs{Follow: false})
		if err := bw.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "events.watch", Args: rawArgs}); err != nil {
			t.Fatal(err)
		}
		rf, err := proto.Read(br)
		if err != nil || rf.Type != proto.TResponse {
			t.Fatalf("read response: %v %+v", err, rf)
		}
		var resp proto.Response
		if err := json.Unmarshal(rf.Payload, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.OK || resp.Error == nil || resp.Error.Code != "forbidden" {
			t.Fatalf("events.watch inside session = %+v, want OK=false code=forbidden", resp)
		}
	}

	t.Run("inside_session_unit", func(t *testing.T) {
		uid := os.Getuid()
		inside := []byte(fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", uid, uid, brand.Slice, UnitName("zzzz2222")))
		oldSelf, oldNS := SelfCgroup, CgroupNamespaces
		SelfCgroup = func() ([]byte, error) { return inside, nil }
		CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
		t.Cleanup(func() { SelfCgroup, CgroupNamespaces = oldSelf, oldNS })
		checkRefused(t)
	})

	t.Run("foreign_cgroup_namespace", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("cgroup namespace check applies on Linux")
		}
		outside := []byte("0::/user.slice/user-1000.slice/session-1.scope\n")
		oldSelf, oldNS := SelfCgroup, CgroupNamespaces
		SelfCgroup = func() ([]byte, error) { return outside, nil }
		CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[2]", nil }
		t.Cleanup(func() { SelfCgroup, CgroupNamespaces = oldSelf, oldNS })
		checkRefused(t)
	})
}
