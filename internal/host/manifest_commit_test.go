package host

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

type commitDirectoryFixture struct {
	syncErr, closeErr error
	synced, closed    bool
}

func (f *commitDirectoryFixture) Sync() error  { f.synced = true; return f.syncErr }
func (f *commitDirectoryFixture) Close() error { f.closed = true; return f.closeErr }

func TestManifestSaveNeverReportsIgnoredParentCommitFailure(t *testing.T) {
	for _, stage := range []string{"open", "sync", "close", "unsupported"} {
		t.Run(stage, func(t *testing.T) {
			m := &Manifest{Schema: 1, ID: api.NewSessionID(), Name: "before"}
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(m.ID)) })
			original := openManifestDirectory
			defer func() { openManifestDirectory = original }()
			fault := error(syscall.EIO)
			if stage == "unsupported" {
				fault = syscall.EINVAL // unsupported native sync is not a waiver
			}
			directory := &commitDirectoryFixture{}
			openManifestDirectory = func(string) (manifestDirectory, error) {
				if stage == "open" {
					return nil, fault
				}
				if stage == "close" {
					directory.closeErr = fault
				} else {
					directory.syncErr = fault
				}
				return directory, nil
			}
			m.Name = "prospective"
			if err := m.Save(); !errors.Is(err, fault) {
				t.Fatal("a failed parent commit could produce a durable ACK", err)
			}
			if stage != "open" && (!directory.synced || !directory.closed) {
				t.Fatal("a parent sync error leaked the owned directory handle")
			}
		})
	}
}

func TestManifestTempCloseFailureDoesNotPublishOrLeak(t *testing.T) {
	m := &Manifest{Schema: 1, ID: api.NewSessionID(), Name: "before"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(m.ID)) })
	original := closeManifestTemp
	defer func() { closeManifestTemp = original }()
	closeManifestTemp = func(file *os.File) error {
		_ = original(file)
		return syscall.EIO
	}
	m.Name = "prospective"
	if err := m.Save(); !errors.Is(err, syscall.EIO) {
		t.Fatal("temporary file close failure was acknowledged", err)
	}
	actual, err := LoadManifest(m.ID)
	if err != nil || actual.Name != "before" {
		t.Fatal("failed temporary close published its prospective content", err)
	}
	entries, err := os.ReadDir(paths.SessionDir(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".manifest-") {
			t.Fatal("failed close retained an owned temporary manifest")
		}
	}
}
