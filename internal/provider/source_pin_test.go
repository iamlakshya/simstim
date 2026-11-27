package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/files"
)

func sourcePinIdentity(t *testing.T, path string) string {
	t.Helper()
	file, err := pinSourceConfigDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	identity, err := files.Identity(file)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestConfigPinActualExistingAndAbsentWitnesses(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(parent, "actually-selected-absent-config")
	source := client.MachineConfigSource{Role: client.MachineConfigAbsent, Path: absent, ParentID: sourcePinIdentity(t, parent)}
	pin, err := PinSourceConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := pin.Revalidate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatal("absence inspection created a provider configuration directory")
	}
	if err := os.Mkdir(absent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := pin.Revalidate(); err == nil {
		t.Fatal("new configuration silently renewed an old absence witness")
	}
	pin.Close()
	existing := client.MachineConfigSource{Role: client.MachineConfigExisting, Path: absent, ObjectID: sourcePinIdentity(t, absent)}
	pin, err = PinSourceConfig(existing)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	if err := os.Rename(absent, filepath.Join(parent, "retained-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(absent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := pin.Revalidate(); err == nil {
		t.Fatal("replacement directory renewed the original native pin")
	}
}

func TestConfigPinUnusedDoesNotInventAPathAndUnknownRefuses(t *testing.T) {
	pin, err := PinSourceConfig(client.MachineConfigSource{Role: client.MachineConfigUnused})
	if err != nil || pin.file != nil || pin.root != nil {
		t.Fatalf("unused config invented a directory: %+v %v", pin, err)
	}
	if err := pin.Revalidate(); err != nil {
		t.Fatal(err)
	}
	pin.Close()
	if err := pin.Revalidate(); err == nil {
		t.Fatal("closed metadata witness was usable")
	}
	for _, source := range []client.MachineConfigSource{{}, {Role: "future"}, {Role: client.MachineConfigUnused, Path: t.TempDir()},
		{Role: client.MachineConfigExisting, Path: "relative", ObjectID: "unknown"}} {
		if pin, err := PinSourceConfig(source); err == nil || pin != nil {
			t.Fatalf("unknown configuration witness became usable: %+v %v", pin, err)
		}
	}
}
