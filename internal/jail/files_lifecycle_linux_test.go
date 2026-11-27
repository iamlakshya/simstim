package jail

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStartRefusesClosedFilesBeforeCreatingAStage(t *testing.T) {
	for _, name := range []string{"Stdin", "Stdout", "Stderr", "Spec.Files"} {
		t.Run(name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "closed")
			if err != nil {
				t.Fatal(err)
			}
			c := lifecycleCmd(t)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			c.ctx = ctx
			switch name {
			case "Stdin":
				c.Stdin = file
			case "Stdout":
				c.Stdout = file
			case "Stderr":
				c.Stderr = file
			case "Spec.Files":
				c.files = []*os.File{file}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Start(); err == nil {
				c.killGroup()
				_ = c.Wait()
				t.Fatal("Start accepted a closed file")
			} else if !strings.Contains(err.Error(), "closed") || hintOf(err) == "" {
				t.Errorf("closed %s = %v, want a refusal with a hint", name, err)
			}
			if c.cmd.Process != nil || c.Pid() != 0 {
				t.Errorf("Start created a stage before refusing closed %s", name)
			}
		})
	}
}

func TestStartKeepsInheritedFileOrderAfterCallerCloses(t *testing.T) {
	var files []*os.File
	for _, text := range []string{"first", "second"} {
		name := filepath.Join(t.TempDir(), "input")
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		defer func() { _ = file.Close() }()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	process := exec.CommandContext(context.WithoutCancel(ctx), "/bin/sh", "-c", "cat <&5 >/dev/null; cat <&3; cat <&4; exit 7")
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	c := &Cmd{ctx: ctx, cmd: process, files: files, setup: []byte("{}\n"), abort: make(chan struct{}), done: make(chan struct{})}
	output, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	closeFiles(files)
	bytes, readErr := io.ReadAll(output)
	var ended *ExitError
	if err := c.Wait(); !errors.As(err, &ended) || ended.Code != 7 || readErr != nil || string(bytes) != "firstsecond" {
		t.Errorf("inherited file order = %q, read=%v, wait=%v", bytes, readErr, err)
	}
}

func TestStageRefusesAMissingInheritedSlot(t *testing.T) {
	if os.Getenv("JAILTEST_CLOSED_INHERITED_SLOT") == "1" {
		if err := unix.Close(3); err != nil {
			os.Exit(2)
		}
		os.Exit(runStage([]string{stageInit, "4"}))
	}
	inMode(t, testMode{"landlock", false})
	file, err := os.CreateTemp(t.TempDir(), "inherited")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	c, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", "exit 0"}, Binds: libBinds(),
		Files: []*os.File{file}, Seccomp: ProfileConverter, Env: probeEnv})
	if err != nil {
		t.Fatal(err)
	}
	defer c.closePins()
	setupFile, err := os.CreateTemp(t.TempDir(), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = setupFile.Close() }()
	if _, err := setupFile.Write(c.setup); err != nil {
		t.Fatal(err)
	}
	if _, err := setupFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = statusR.Close() }()
	defer func() { _ = statusW.Close() }()
	child := exec.CommandContext(t.Context(), testExe(t), "-test.run=^TestStageRefusesAMissingInheritedSlot$")
	child.Env = append(probeEnv, "JAILTEST_CLOSED_INHERITED_SLOT=1")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.ExtraFiles = append([]*os.File{file, setupFile, statusW}, c.pins...)
	output, err := child.CombinedOutput()
	var ended *exec.ExitError
	if !errors.As(err, &ended) || ended.ExitCode() != setupFailed || !strings.Contains(string(output), "inherited file is closed") {
		t.Errorf("stage with a missing inherited slot = %v, %q; want exit125 and its reason", err, output)
	}
}
