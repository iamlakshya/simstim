//go:build unix

package cli

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestCompletionMainStaysSilentOnSignal(t *testing.T) {
	for _, name := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
			t.Run(name+"/"+sig.String(), func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
				cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join([]string{name, "attach", ""}, mainArgsSep), mainSceneEnv+"=completion-signal", "NO_COLOR=1")
				stdout, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = writer, &stderr
				if err := cmd.Start(); err != nil {
					_ = stdout.Close()
					_ = writer.Close()
					t.Fatal(err)
				}
				_ = writer.Close()
				done := make(chan struct{})
				go func() {
					_ = cmd.Wait()
					close(done)
				}()
				t.Cleanup(func() {
					select {
					case <-done:
					default:
						_ = cmd.Process.Kill()
						<-done
					}
					_ = stdout.Close()
				})
				reader := bufio.NewReader(stdout)
				ready := make(chan string, 1)
				go func() {
					line, _ := reader.ReadString('\n')
					ready <- line
				}()
				select {
				case line := <-ready:
					if line != "completion ready\n" {
						t.Fatalf("completion did not reach its signal gate: %q", line)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("completion did not start")
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("completion did not stop after the signal")
				}
				rest, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if code := cmd.ProcessState.ExitCode(); code != 130 || stderr.Len() != 0 || !strings.Contains(string(rest), ":4") {
					t.Errorf("completion exit=%d stdout=%q stderr=%q", code, rest, stderr.String())
				}
			})
		}
	}
}
