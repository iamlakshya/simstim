package jail

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func ruleMemfd(t *testing.T, body []byte, mode uint32) *os.File {
	t.Helper()
	fd, err := unix.MemfdCreate("file-rule-test", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC|unix.MFD_EXEC)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "file-rule-test")
	t.Cleanup(func() { file.Close() })
	if _, err := file.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_EXEC|unix.F_SEAL_SEAL); err != nil {
		t.Fatal(err)
	}
	return file
}

// This uses the real canonical jail in both modes. It is not a substitute
// for the separately required actual pinned VMM/KVM and post-exec proofs.
func TestFileRulesPermitOnlyChosenReopensAndSecondExec(t *testing.T) {
	stage := os.Getenv("JAILTEST_FILE_RULE_STAGE")
	if stage != "" {
		if stage != "after-exec" {
			if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
		if stage == "after-exec" {
			value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
			if err != nil || value != 0 {
				os.Exit(2)
			}
		}
		data, err := os.ReadFile("/proc/self/fd/3")
		if err != nil || string(data) != "selected" {
			fmt.Fprintln(os.Stderr, "selected reopen", err)
			os.Exit(2)
		}
		_, otherErr := os.ReadFile("/proc/self/fd/5")
		if os.Getenv("JAILTEST_FILE_RULE_CONTROL") == "1" {
			if otherErr != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
		if otherErr == nil {
			fmt.Fprintln(os.Stderr, "unselected reopen succeeded")
			os.Exit(2)
		}
		uid, err := strconv.Atoi(os.Getenv("JAILTEST_FILE_RULE_UID"))
		if err != nil || os.Getuid() != uid {
			fmt.Fprintln(os.Stderr, "wrong inner identity")
			os.Exit(2)
		}
		if stage == "after-exec" {
			fmt.Fprintln(os.Stdout, "file-rules-ok")
			os.Exit(0)
		}
		unix.CloseOnExec(4)
		unix.CloseOnExec(6)
		err = unix.Exec("/proc/self/fd/4", []string{"file-rule-second", "-test.run=^TestFileRulesPermitOnlyChosenReopensAndSecondExec$"}, []string{"JAILTEST_FILE_RULE_STAGE=after-exec", "JAILTEST_FILE_RULE_UID=" + strconv.Itoa(uid)})
		fmt.Fprintln(os.Stderr, "selected second exec", err)
		os.Exit(2)
	}
	exe := testExe(t)
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range bothModes {
		for _, sub := range []bool{false, true} {
			if sub && !mode.userns {
				continue
			}
			t.Run(fmt.Sprintf("%s/subuid-%v", mode.name, sub), func(t *testing.T) {
				inMode(t, mode)
				if sub {
					if _, ok := lookupSubID(); !ok {
						t.Skip("no canonical subordinate mapping; native subuid proof remains open")
					}
				}
				selected := ruleMemfd(t, []byte("selected"), 0o444)
				program := ruleMemfd(t, body, 0o111)
				other := ruleMemfd(t, []byte("unselected"), 0o444)
				helper, err := os.Open(exe)
				if err != nil {
					t.Fatal(err)
				}
				defer helper.Close()
				files := []*os.File{selected, program, other, helper}
				control := exec.CommandContext(t.Context(), exe, "-test.run=^TestFileRulesPermitOnlyChosenReopensAndSecondExec$")
				control.ExtraFiles = files
				control.Env = []string{"JAILTEST_FILE_RULE_STAGE=read", "JAILTEST_FILE_RULE_CONTROL=1"}
				if output, err := control.CombinedOutput(); err != nil {
					t.Fatalf("unconfined self-proc control: %v %s", err, output)
				}
				for _, missing := range []string{"", "data", "program"} {
					var rules []FileRuleV1
					if missing != "data" {
						rules = append(rules, FileRuleV1{FD: 3, Role: FileDataV1, Mode: 0o444, Access: Read})
					}
					if missing != "program" {
						rules = append(rules, FileRuleV1{FD: 4, Role: FileProgramV1, Mode: 0o111, Access: Read | Exec})
					}
					uid := 0
					if !mode.userns {
						uid = os.Getuid()
					}
					if sub {
						uid = 1
					}
					command, err := Command(t.Context(), Spec{Argv: []string{"/proc/self/fd/6", "-test.run=^TestFileRulesPermitOnlyChosenReopensAndSecondExec$"}, Files: files, FileRulesV1: rules, Binds: libBinds(), Subuid: sub, Net: NetNone, Seccomp: ProfileVMM, Env: []string{"JAILTEST_FILE_RULE_STAGE=read", "JAILTEST_FILE_RULE_UID=" + strconv.Itoa(uid)}})
					if err != nil {
						t.Fatal(err)
					}
					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					command.Stdout = writer
					command.Stderr = writer
					if err := command.Start(); err != nil {
						reader.Close()
						writer.Close()
						t.Fatal(err)
					}
					writer.Close()
					output, readErr := io.ReadAll(reader)
					reader.Close()
					waitErr := command.Wait()
					if readErr != nil {
						t.Fatal(readErr)
					}
					if missing == "" && (waitErr != nil || !strings.Contains(string(output), "file-rules-ok")) {
						t.Fatalf("selected file/exec rights: %v %s", waitErr, output)
					}
					if missing != "" && waitErr == nil {
						t.Fatalf("missing %s grant still allowed second exec and data reopens: %s", missing, output)
					}
				}
			})
		}
	}
}
