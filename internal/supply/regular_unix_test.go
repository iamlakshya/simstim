//go:build unix

package supply

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupplyReadersRejectFIFOWithoutWaitingForWriter(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lock", "pin", "item"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "fifo")
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSupplySpecialReaderChild$")
			cmd.Env = append(os.Environ(), "SUPPLY_TEST_SPECIAL_PATH="+p, "SUPPLY_TEST_READER="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("%s waited for a FIFO writer: %v", name, ctx.Err())
			}
			if err != nil {
				t.Fatalf("%s FIFO refusal: %v\n%s", name, err, out)
			}
		})
	}
}

func TestSupplySpecialReaderChild(t *testing.T) {
	p := os.Getenv("SUPPLY_TEST_SPECIAL_PATH")
	if p == "" {
		t.Skip("only run by the FIFO regression parent")
	}
	readers, _ := regularReaders(t)
	read := readers[os.Getenv("SUPPLY_TEST_READER")]
	if read == nil {
		t.Fatal("unknown supply reader")
	}
	if err := read(p); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO = %v, want an immediate regular-file refusal", err)
	}
}
