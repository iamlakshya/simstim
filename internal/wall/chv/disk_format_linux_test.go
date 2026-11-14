//go:build linux

package chv

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestDiskFormatterCommandFixesTypeOwnerDescriptorAndEnvironment(t *testing.T) {
	dir := diskTestDir(t)
	file, err := os.CreateTemp(dir, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	t.Setenv("LD_PRELOAD", "not inherited")
	t.Setenv("MKE2FS_CONFIG", "not inherited")
	command, err := diskFormatCommand(context.Background(), "/usr/sbin/mke2fs", file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mkfs.ext4", "-q", "-F", "-E", "root_owner=1000:1000", "/proc/self/fd/3"}
	if command.Path != "/usr/sbin/mke2fs" || !reflect.DeepEqual(command.Args, want) || !reflect.DeepEqual(command.Env, []string{"LC_ALL=C", "LANG=C"}) || len(command.ExtraFiles) != 1 || command.ExtraFiles[0] != file || command.SysProcAttr != nil || command.WaitDelay != 5*time.Second {
		t.Fatalf("formatter boundary changed: %+v", command)
	}
}

func TestDiskFormatterRefusesUnsafeProgramOrDescriptorBeforeExec(t *testing.T) {
	dir := diskTestDir(t)
	file, err := os.CreateTemp(dir, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, path := range []string{"mkfs.ext4", "/usr/sbin/../sbin/mkfs.ext4", "/usr/sbin/mkfs\x00.ext4"} {
		if _, err := diskFormatCommand(context.Background(), path, file); err == nil {
			t.Fatalf("unsafe formatter path %q", path)
		}
	}
	if _, err := diskFormatCommand(context.Background(), "/usr/sbin/mkfs.ext4", nil); err == nil {
		t.Fatal("missing output descriptor accepted")
	}
}

func TestDiskFormatterDiagnosticIsBoundedAndKeepsTail(t *testing.T) {
	var diagnostic diskDiagnostic
	prefix := bytes.Repeat([]byte("x"), 70<<10)
	if n, err := diagnostic.Write(prefix); err != nil || n != len(prefix) {
		t.Fatal(n, err)
	}
	if _, err := diagnostic.Write([]byte("last line")); err != nil {
		t.Fatal(err)
	}
	if len(diagnostic.data) != 64<<10 || !bytes.HasSuffix(diagnostic.data, []byte("last line")) {
		t.Fatal("formatter diagnostics grew or lost the final line")
	}
}
