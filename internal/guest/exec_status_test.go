package guest

import (
	"io/fs"
	"os"
	"os/exec"
	"testing"
)

func TestExecStartErrorsFollowCommandContract(t *testing.T) {
	for _, row := range []struct {
		err  error
		code int
	}{{fs.ErrNotExist, 127}, {fs.ErrPermission, 126}, {&os.PathError{Op: "fork/exec", Path: "missing", Err: fs.ErrNotExist}, 127}, {fs.ErrInvalid, 125}} {
		if got := execStartCode(row.err); got != row.code {
			t.Fatalf("%v =>%d want%d", row.err, got, row.code)
		}
	}
	if got := execWaitCode(nil); got != 0 {
		t.Fatal(got)
	}
	if got := execWaitCode(&exec.Error{Name: "missing", Err: fs.ErrNotExist}); got != 125 {
		t.Fatal(got)
	}
}
func TestGuestCommandPathNeverFallsBackToInitPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, code, err := guestExecutable("unknown-tool", []string{"PATH=relative:"}, t.TempDir()); err == nil || code != 127 {
		t.Fatalf("code%d err%v", code, err)
	}
}
