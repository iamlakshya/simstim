//go:build linux || darwin

package files

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func putState(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func statePlan() StatePlan { return StatePlan{Files: []string{".agent/threads"}} }
func newCapture(t *testing.T, session string) *Capture {
	t.Helper()
	c, err := BeginCapture(session, statePlan())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func captureHome(t *testing.T, c *Capture, home string) {
	t.Helper()
	if err := CaptureFamilies(home, c.Incoming, c.plan.Files); err != nil {
		t.Fatal(err)
	}
}
func readState(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStateEpochNeedsOwnedExitAndExactNonce(t *testing.T) {
	session, home := t.TempDir(), t.TempDir()
	c := newCapture(t, session)
	putState(t, home, ".agent/threads", "old")
	captureHome(t, c, home)
	for _, proof := range []struct {
		clean bool
		nonce string
	}{{false, c.Nonce}, {true, "../CURRENT"}, {true, "00000000000000000000000000000000"}} {
		if err := c.Commit(proof.clean, proof.nonce); err == nil {
			t.Fatal("unproved capture published")
		}
		if _, err := os.Stat(filepath.Join(session, "state-files", "CURRENT")); !os.IsNotExist(err) {
			t.Fatal("CURRENT changed before a clean exit")
		}
	}
	if err := c.Commit(true, c.Nonce); err != nil {
		t.Fatal(err)
	}
	next := newCapture(t, session)
	if got := readState(t, next.Restore, ".agent/threads"); got != "old" {
		t.Fatal(got)
	}
	if strings.HasPrefix(next.Incoming, next.Restore+string(os.PathSeparator)) {
		t.Fatal("capture area is inside committed state")
	}
}

func TestInterruptedFamilyDoesNotReplaceTheLastCleanEpoch(t *testing.T) {
	session, home := t.TempDir(), t.TempDir()
	first := newCapture(t, session)
	putState(t, home, ".agent/threads", "old-main")
	putState(t, home, ".agent/threads-wal", "old-wal")
	captureHome(t, first, home)
	if err := first.Commit(true, first.Nonce); err != nil {
		t.Fatal(err)
	}
	current := readState(t, filepath.Join(session, "state-files"), "CURRENT")
	second := newCapture(t, session)
	putState(t, second.Incoming, ".agent/threads", "new-main")
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(second.Incoming, ".agent/threads-wal")); err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(true, second.Nonce); err == nil {
		t.Fatal("partial family published")
	}
	if got := readState(t, filepath.Join(session, "state-files"), "CURRENT"); got != current {
		t.Fatal("failed copy replaced CURRENT")
	}
	third := newCapture(t, session)
	if got := readState(t, third.Restore, ".agent/threads-wal"); got != "old-wal" {
		t.Fatal(got)
	}
}

func TestStateRenameAndWholeFamilyDeletion(t *testing.T) {
	session, home := t.TempDir(), t.TempDir()
	c := newCapture(t, session)
	putState(t, home, ".agent/new", "replacement")
	if err := os.Rename(filepath.Join(home, ".agent/new"), filepath.Join(home, ".agent/threads")); err != nil {
		t.Fatal(err)
	}
	putState(t, home, ".agent/threads-journal", "journal")
	captureHome(t, c, home)
	if err := c.Commit(true, c.Nonce); err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	next := newCapture(t, session)
	if err := RestoreFamilies(restored, next.Restore, next.plan.Files); err != nil {
		t.Fatal(err)
	}
	if got := readState(t, restored, ".agent/threads"); got != "replacement" {
		t.Fatal(got)
	}
	if err := os.Remove(filepath.Join(restored, ".agent/threads")); err != nil {
		t.Fatal(err)
	}
	captureHome(t, next, restored)
	if err := next.Commit(true, next.Nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(session, "state-files/epochs/e-"+c.Nonce)); !os.IsNotExist(err) {
		t.Fatal("deleted family remains in an old kept epoch")
	}
	final := newCapture(t, session)
	for _, suffix := range sidecars {
		if _, err := os.Stat(filepath.Join(final.Restore, ".agent/threads"+suffix)); !os.IsNotExist(err) {
			t.Fatal("deleted family revived", suffix)
		}
	}
}

func TestMissingStateFileIsNotCreatedAsADirectory(t *testing.T) {
	c := newCapture(t, t.TempDir())
	home := t.TempDir()
	if err := RestoreFamilies(home, c.Restore, c.plan.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agent/threads")); !os.IsNotExist(err) {
		t.Fatal("missing state file was created")
	}
	for _, bad := range []StatePlan{{Files: []string{".profile"}}, {Dirs: []string{".agent"}, Files: []string{".agent/threads"}}, {Files: []string{"../outside"}}, {Files: []string{".claude/settings.json"}}} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("unsafe plan accepted: %+v", bad)
		}
	}
}

func pythonState(t *testing.T, script string, args ...string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is needed for the real SQLite fixture")
	}
	cmd := exec.Command(python, append([]string{"-c", script}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQLite fixture: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSQLiteWALSnapshotPreservesRowsAndCheckpoint(t *testing.T) {
	session, home := t.TempDir(), t.TempDir()
	db := filepath.Join(home, ".agent/threads")
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	pythonState(t, `import os,sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.execute('pragma journal_mode=wal')
c.execute('pragma wal_autocheckpoint=0')
c.execute('create table history(id integer)')
c.commit()
c.execute('insert into history values(41)')
c.commit()
os._exit(0)`, db)
	if st, err := os.Stat(db + "-wal"); err != nil || st.Size() == 0 {
		t.Fatal("fixture did not leave an uncheckpointed WAL")
	}
	first := newCapture(t, session)
	captureHome(t, first, home)
	if err := first.Commit(true, first.Nonce); err != nil {
		t.Fatal(err)
	}
	next := newCapture(t, session)
	resumed := t.TempDir()
	if err := RestoreFamilies(resumed, next.Restore, next.plan.Files); err != nil {
		t.Fatal(err)
	}
	got := pythonState(t, `import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
assert c.execute('pragma integrity_check').fetchone()[0]=='ok'
assert c.execute('select id from history').fetchall()==[(41,)]
c.execute('insert into history values(42)')
c.commit()
c.execute('pragma wal_checkpoint(truncate)')
c.close()
print('41,42')`, filepath.Join(resumed, ".agent/threads"))
	if got != "41,42" {
		t.Fatal(got)
	}
	captureHome(t, next, resumed)
	if err := next.Commit(true, next.Nonce); err != nil {
		t.Fatal(err)
	}
	final := newCapture(t, session)
	lastHome := t.TempDir()
	if err := RestoreFamilies(lastHome, final.Restore, final.plan.Files); err != nil {
		t.Fatal(err)
	}
	pythonState(t, `import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
assert c.execute('pragma integrity_check').fetchone()[0]=='ok'
assert c.execute('select id from history order by id').fetchall()==[(41,),(42,)]`, filepath.Join(lastHome, ".agent/threads"))
}

func TestStateMountRolesCannotExposeOrWriteCommittedParents(t *testing.T) {
	session, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := newCapture(t, session)
	if err := ValidateStateMounts(session, capture.Restore, capture.Incoming); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ restore, incoming string }{
		{filepath.Join(session, "state-files"), capture.Incoming},
		{filepath.Join(session, "state-files", "epochs"), capture.Incoming},
		{capture.Restore, capture.Restore},
		{capture.Restore, filepath.Join(session, "state-files", "epochs", "e-"+capture.Nonce)},
		{capture.Restore, filepath.Join(session, "state-incoming", strings.ToUpper(capture.Nonce))},
	} {
		if err := ValidateStateMounts(session, test.restore, test.incoming); err == nil {
			t.Fatalf("unsafe state roles accepted: %+v", test)
		}
	}
}

func TestStateRestoreLeavesShellProfilesAndAgentSettingsBehind(t *testing.T) {
	home, session := t.TempDir(), t.TempDir()
	for _, name := range []string{".bashrc", ".profile", ".claude/settings.json", ".config/agent/hooks/start"} {
		putState(t, home, name, "must-not-return")
	}
	putState(t, home, ".agent/threads", "resume-state")
	capture := newCapture(t, session)
	captureHome(t, capture, home)
	if err := capture.Commit(true, capture.Nonce); err != nil {
		t.Fatal(err)
	}
	next := newCapture(t, session)
	fresh := t.TempDir()
	if err := RestoreFamilies(fresh, next.Restore, next.plan.Files); err != nil {
		t.Fatal(err)
	}
	if got := readState(t, fresh, ".agent/threads"); got != "resume-state" {
		t.Fatal(got)
	}
	for _, name := range []string{".bashrc", ".profile", ".claude/settings.json", ".config/agent/hooks/start"} {
		if _, err := os.Stat(filepath.Join(fresh, name)); !os.IsNotExist(err) {
			t.Fatal("unselected executable state returned", name)
		}
	}
}
