//go:build linux

package guest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func TestPurgeReaderRefusesANetworkInterfaceV1(t *testing.T) {
	for _, names := range [][]string{nil, {"eth0"}, {"lo", "eth0"}, {"lo", "tap0"}} {
		if purgeReaderNetworkDevicesV1(names) == nil {
			t.Fatal("another native network layout selected the fixed reader", names)
		}
	}
	if err := purgeReaderNetworkDevicesV1([]string{"lo"}); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeReaderRetainsNativePivotBeforeLateErrorV1(t *testing.T) {
	fs, _ := vmMountFixture(t)
	wanted := errors.New("chdir failed after native pivot")
	fs.ops.chdir = func(string) error { return wanted }
	if err := fs.pivotPurgeReaderV1(); err != wanted || !fs.pivoted || fs.back {
		t.Fatal("a late error lost the reader's actual native pivot", err, fs.pivoted, fs.back)
	}
}

func TestPurgeReaderDiskRolesRejectOrdinaryMutableSessionV1(t *testing.T) {
	data := map[string]string{}
	for name, serial := range map[string]string{"vda": "tools", "vdb": "image", "vdc": purgeReaderCopySerialV1, "vdd": purgeReaderScratchSerialV1} {
		data["/sys/block/"+name+"/serial"], data["/sys/block/"+name+"/ro"] = serial, "1"
	}
	data["/sys/block/vdd/ro"] = "0"
	read := func(path string) (string, error) {
		value, ok := data[path]
		if !ok {
			return "", fmt.Errorf("not supplied")
		}
		return value, nil
	}
	if err := verifyPurgeReaderDisksV1(read); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ path, value string }{{"/sys/block/vdc/ro", "0"}, {"/sys/block/vdc/serial", "agents"}, {"/sys/block/vdd/serial", "session"}, {"/sys/block/vdd/ro", "1"}, {"/sys/block/vda/ro", "0"}} {
		previous := data[mutation.path]
		data[mutation.path] = mutation.value
		if verifyPurgeReaderDisksV1(read) == nil {
			t.Fatal("ordinary or changed disk data selected a reader source", mutation)
		}
		data[mutation.path] = previous
	}
}

func TestPurgeReaderMountsOnlyImmutableWorkAndFreshScratchV1(t *testing.T) {
	fs, calls := vmMountFixture(t)
	for _, name := range []string{"mnt/reader-copy/work", "mnt/reader-scratch"} {
		if err := os.MkdirAll(filepath.Join(fs.base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := fs.layoutPurgeReaderV1(); err != nil {
		t.Fatal(err)
	}
	copyRO, scratchRW, workRO := false, false, false
	for _, call := range *calls {
		if call.target == fs.boot("mnt/reader-copy") {
			copyRO = call.flags&(unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC) == unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC && call.data == "noload"
		}
		if call.target == fs.boot("mnt/reader-scratch") {
			scratchRW = call.flags&unix.MS_RDONLY == 0
		}
		if call.target == filepath.Join(fs.root, "work") && call.flags&unix.MS_REMOUNT != 0 {
			workRO = call.flags&unix.MS_RDONLY != 0
		}
		if call.target == fs.boot("mnt/session") || call.target == fs.boot("mnt/agents") || call.kind == "devpts" {
			t.Fatal("the reader mounted ordinary mutable state, agents or a terminal", call)
		}
	}
	if !copyRO || !scratchRW || !workRO {
		t.Fatal("the fixed copy/scratch/work mount roles are incomplete", *calls)
	}
	for _, name := range []string{"dev/tty", "dev/ptmx", "dev/vdc", ".persist", "opt/agents"} {
		if _, err := os.Lstat(filepath.Join(fs.root, name)); !os.IsNotExist(err) {
			t.Fatal("the fixed reader exposed another execution or disk surface", name, err)
		}
	}
}

func TestPurgeReaderRefusesPriorScratchAndSymlinkedWorkV1(t *testing.T) {
	for _, prior := range []bool{false, true} {
		fs, _ := vmMountFixture(t)
		if err := os.MkdirAll(fs.boot("mnt/reader-copy"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(fs.boot("mnt/reader-scratch"), 0o700); err != nil {
			t.Fatal(err)
		}
		if prior {
			if err := os.Mkdir(fs.boot("mnt/reader-copy/work"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fs.boot("mnt/reader-scratch/previous"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Symlink("elsewhere", fs.boot("mnt/reader-copy/work")); err != nil {
			t.Fatal(err)
		}
		if fs.layoutPurgeReaderV1() == nil {
			t.Fatal("a previous scratch or redirected copied workspace reached reader setup", prior)
		}
	}
}

func TestPurgeReaderControlDataCannotConstructItsNativeOwnerV1(t *testing.T) {
	p := &purgeReaderV1{init: &Init{}}
	raw := []byte(`{"v":1,"primary":"primary2","repositories":[{"name":"repo","saved":[]}]}`)
	answered := false
	p.request(proto.Request{ID: 1, Op: proto.CtlPurgeWorkInspectV1, Args: raw}, func(value any, err error) {
		answered = true
		if err == nil || value != nil {
			t.Fatal("a protected-plan DTO invented an actual original reader boot")
		}
	})
	if !answered || p.used || p.active != nil {
		t.Fatal("unissued native owner changed reader state")
	}
}
