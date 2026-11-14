// Package probetest supports actual owned memory-guard test processes only.
// No production caller imports it and no parent test process changes a limit.
package probetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/secretmem"
)

const ChildFlag = "--owned-memory-guard-probe"

type Report struct {
	Entry                string           `json:"entry"`
	OS                   string           `json:"os"`
	Arch                 string           `json:"arch"`
	Inherited            secretmem.State  `json:"inherited"`
	Prepared             secretmem.State  `json:"prepared"`
	Observed             *secretmem.State `json:"observed,omitempty"`
	Phase                string           `json:"phase,omitempty"`
	CorePreparation      string           `json:"core_preparation"`
	DumpPreparation      string           `json:"dump_preparation"`
	SoftSetterObservable bool             `json:"soft_setter_observable"`
	HardSetterObservable bool             `json:"hard_setter_observable"`
	DumpSetterObservable bool             `json:"dump_setter_observable"`
	Failure              string           `json:"failure,omitempty"`
}

type Child struct {
	Report   Report
	reported bool
}

func Mode() string {
	if len(os.Args) == 3 && os.Args[1] == ChildFlag {
		return os.Args[2]
	}
	return ""
}

// BeginChild is never called by a parent test. A positive baseline is not
// fabricated when an inherited hard0 prevents raising the current limit.
func BeginChild(entry string) *Child {
	child := &Child{Report: Report{Entry: entry, OS: runtime.GOOS, Arch: runtime.GOARCH}}
	if Mode() == "" {
		child.Fail("not_an_explicit_probe_child")
	}
	state, err := secretmem.Inspect()
	if err != nil {
		child.Fail("initial_inspection_failed")
	}
	child.Report.Inherited = state
	child.Report.CorePreparation = prepareCoreBaseline()
	child.Report.DumpPreparation = prepareDumpBaseline()
	state, err = secretmem.Inspect()
	if err != nil {
		child.Fail("baseline_inspection_failed")
	}
	child.Report.Prepared = state
	child.Report.SoftSetterObservable = state.CoreSoft != 0
	child.Report.HardSetterObservable = state.CoreHard != 0
	child.Report.DumpSetterObservable = state.Dumpable != nil && *state.Dumpable != 0
	return child
}

func (c *Child) Observe(phase string) {
	state, err := secretmem.Inspect()
	if err != nil {
		c.Fail("entry_inspection_failed")
	}
	c.Report.Observed, c.Report.Phase = &state, phase
	c.emit()
}

func (c *Child) ObserveAndExit(phase string) {
	c.Observe(phase)
	os.Exit(0)
}

func (c *Child) Fail(stage string) {
	c.Report.Failure = stage
	c.emit()
	os.Exit(2)
}

func (c *Child) emit() {
	if c.reported {
		os.Exit(3)
	}
	c.reported = true
	if json.NewEncoder(os.Stdout).Encode(c.Report) != nil {
		os.Exit(4)
	}
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("probe output exceeded its bound")
	}
	return b.Buffer.Write(p)
}

// Environment does not inherit user credentials, unknown XDG paths, shell
// hooks or configuration. All seven XDG variables are explicit owned paths.
func Environment(t testing.TB) []string {
	t.Helper()
	root := t.TempDir()
	paths := map[string]string{
		"HOME": "home", "USERPROFILE": "home", "GNUPGHOME": "gnupg",
		"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state",
		"XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run", "XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_DIRS": "data-dirs",
		"TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp",
	}
	env := []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"), "GIT_CONFIG_SYSTEM=" + filepath.Join(root, "system-gitconfig")}
	for key, relative := range paths {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal("cannot create owned probe state")
		}
		env = append(env, key+"="+path)
	}
	return env
}

// Run owns exactly one Cmd.Run waiter. Its context uses that Cmd's owned
// process handle for cancellation. No PID lookup or second Wait is involved.
func Run(t testing.TB, mode string, files ...*os.File) Report {
	t.Helper()
	parentBefore, inspectErr := secretmem.Inspect()
	if inspectErr != nil {
		t.Fatal("cannot inspect read-only parent baseline")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot find the current test executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, ChildFlag, mode)
	cmd.Env = Environment(t)
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "HOME=") {
			cmd.Dir = strings.TrimPrefix(entry, "HOME=")
		}
	}
	cmd.ExtraFiles = files
	stdout, stderr := &boundedOutput{limit: 32 << 10}, &boundedOutput{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	parentAfter, inspectErr := secretmem.Inspect()
	if inspectErr != nil || !sameState(parentBefore, parentAfter) {
		t.Fatal("child probe changed its parent protection state")
	}
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		t.Fatal("owned probe did not return exactly one bounded state report")
	}
	if err != nil || report.Failure != "" {
		t.Fatalf("owned probe failed: entry=%s phase=%s failure=%s", report.Entry, report.Phase, report.Failure)
	}
	return report
}

func sameState(a, b secretmem.State) bool {
	if a.CoreSoft != b.CoreSoft || a.CoreHard != b.CoreHard {
		return false
	}
	if a.Dumpable == nil || b.Dumpable == nil {
		return a.Dumpable == nil && b.Dumpable == nil
	}
	return *a.Dumpable == *b.Dumpable
}

func RequireProtected(t testing.TB, report Report, phase string) {
	t.Helper()
	if report.Observed == nil || report.Phase != phase || report.OS != runtime.GOOS || report.Arch != runtime.GOARCH {
		t.Fatal("actual-child observation is absent or belongs to another phase/platform")
	}
	if report.SoftSetterObservable != (report.Prepared.CoreSoft != 0) || report.HardSetterObservable != (report.Prepared.CoreHard != 0) || report.DumpSetterObservable != (report.Prepared.Dumpable != nil && *report.Prepared.Dumpable != 0) {
		t.Fatal("setter eligibility does not match the actual prepared baseline")
	}
	if report.CorePreparation == "positive_baseline_prepared" && (report.Prepared.CoreSoft == 0 || report.Prepared.CoreHard == 0) {
		t.Fatal("inspection did not reflect the successful positive core baseline")
	}
	if report.DumpPreparation == "dumpable1_prepared" && (report.Prepared.Dumpable == nil || *report.Prepared.Dumpable != 1) {
		t.Fatal("inspection did not reflect the successful Linux dumpability baseline")
	}
	if report.Observed.CoreSoft != 0 || report.Observed.CoreHard != 0 {
		t.Fatal("actual entry left a nonzero core limit before its first read")
	}
	if runtime.GOOS == "linux" {
		if report.Observed.Dumpable == nil || *report.Observed.Dumpable != 0 {
			t.Fatal("actual Linux entry remained dumpable before its first read")
		}
	} else if report.Observed.Dumpable != nil {
		t.Fatal("portable probe manufactured Linux dumpability evidence")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal("cannot preserve public probe report")
	}
	t.Logf("actual-child-guard-report: %s", encoded)
	t.Logf("actual-child protection: entry=%s os=%s arch=%s inherited_core=%d/%d prepared_core=%d/%d core_setter_observable=%t/%t dump_setter_observable=%t", report.Entry, report.OS, report.Arch, report.Inherited.CoreSoft, report.Inherited.CoreHard, report.Prepared.CoreSoft, report.Prepared.CoreHard, report.SoftSetterObservable, report.HardSetterObservable, report.DumpSetterObservable)
	if !report.SoftSetterObservable || !report.HardSetterObservable {
		t.Log("core invariant holds, but inherited/prepared0 does not prove the corresponding setter mutation was killed")
	}
	if runtime.GOOS == "linux" && !report.DumpSetterObservable {
		t.Log("dumpability invariant holds, but this baseline does not prove the dumpability setter mutation was killed")
	}
}
