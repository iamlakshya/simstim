package jail

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestChildrenWithoutProcChildrenUsesOwnedUnreapedChildren(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "self/task/100"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pid, parent int
		comm        string
	}{{100, 1, "init"}, {200, 100, "child"}, {201, 100, "a ) tricky ( child"}, {300, 1, "unrelated"}, {400, 200, "grandchild"}} {
		dir := filepath.Join(root, fmt.Sprint(c.pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Everyone shares the same group in this fixture. Only direct,
		// unreaped children have a PID this init can safely signal.
		stat := fmt.Sprintf("%d (%s) S %d 100 100 0 0", c.pid, c.comm, c.parent)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := childrenIn(root, 100)
	slices.Sort(got)
	if !slices.Equal(got, []int{200, 201}) {
		t.Errorf("children without CONFIG_PROC_CHILDREN = %v, want only direct children200,201", got)
	}
	if err := os.WriteFile(filepath.Join(root, "self/task/100/children"), []byte("201 200"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = childrenIn(root, 100)
	slices.Sort(got)
	if !slices.Equal(got, []int{200, 201}) {
		t.Errorf("task children = %v, want200,201", got)
	}
}

func TestProcessParentRejectsIncompleteStat(t *testing.T) {
	for _, stat := range []string{"", "10 no-comm S 100", "10 (unterminated", "10 (name)", "10 (name) S bad", "10 (name) S -1"} {
		if got := processParent(stat); got != -1 {
			t.Errorf("processParent(%q) = %d, want invalid", stat, got)
		}
	}
}
