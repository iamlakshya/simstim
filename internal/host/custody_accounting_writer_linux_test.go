//go:build linux

package host

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

// These exercise the real original OP/OFD and rooted DATA writer. They do not
// issue a native manager source, fake StartUnit, claim or execution permission.
func custodyAccountingOperationV1(t *testing.T) (*SessionOperation, Manifest, *ResumePreparationV1) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := filepath.Join(root, strings.ToLower(name))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
	m := resumePreparationFixtureV1()
	m.ID = "custody234"
	m.Schema = 1
	m.ResumePreparation = nil
	if err := paths.EnsurePrivateDir(paths.SessionDir(m.ID)); err != nil {
		t.Fatal(err)
	}
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.SessionDir(m.ID), "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	operation, err := BeginSessionOperation(context.Background(), m.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(operation.Close)
	value := &ResumePreparationV1{V: 1, Nonce: strings.Repeat("a", 64), Boot: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", Since: time.Now().UTC(), MemoryMax: m.Limits.Declared}
	return operation, m, value
}

func TestCustodyAccountingLeafPersistsUntilSameOwnerCancellation(t *testing.T) {
	operation, m, value := custodyAccountingOperationV1(t)
	runtime, err := startupRuntimeBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	published, err := writeResumePreparationLeafLockedV1(context.Background(), operation, m.VMNeedV1, runtime, nil, value)
	if err != nil || !published {
		t.Fatal("complete charge was not physically published", published, err)
	}
	for _, native := range []memoryUnitFactsV1{{}, {exists: true, maximum: value.MemoryMax, swap: 0}} {
		current, err := operation.LoadContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		charge, active, err := resumePreparationChargeV1(current, value.Boot, native, memoryUnitFactsV1{})
		if err != nil || !active || charge != value.MemoryMax {
			t.Fatal("before/after native appearance lost or doubled the same charge", charge, active, err)
		}
	}
	other := *value
	other.Nonce = strings.Repeat("b", 64)
	if _, err := writeResumePreparationLeafLockedV1(context.Background(), operation, m.VMNeedV1, runtime, &other, nil); err == nil {
		t.Fatal("another token cleared the original charge")
	}
	current, err := operation.LoadContext(context.Background())
	if err != nil || current.ResumePreparation == nil || current.ResumePreparation.Nonce != value.Nonce {
		t.Fatal("refused cancellation damaged the actual leaf", err)
	}
	published, err = writeResumePreparationLeafLockedV1(context.Background(), operation, m.VMNeedV1, runtime, value, nil)
	if err != nil || !published {
		t.Fatal("same original owner did not settle its data publication", published, err)
	}
}

func TestCustodyAccountingWriterRefusesReplacedRootWithoutTouchingSubstitute(t *testing.T) {
	operation, m, value := custodyAccountingOperationV1(t)
	runtime, err := startupRuntimeBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	name := paths.SessionDir(m.ID)
	if err := os.Rename(name, name+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("replacement must remain untouched\n")
	if err := os.WriteFile(filepath.Join(name, "manifest.json"), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if published, err := writeResumePreparationLeafLockedV1(context.Background(), operation, m.VMNeedV1, runtime, nil, value); err == nil || published {
		t.Fatal("a replacement root renewed the actual OP publication", published, err)
	}
	actual, err := os.ReadFile(filepath.Join(name, "manifest.json"))
	if err != nil || !bytes.Equal(actual, sentinel) {
		t.Fatal("refusal changed the substitute manifest", err)
	}
}
