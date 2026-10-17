package bundle

import (
	"context"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

// SetHostname makes the laptop's name n for the rest of the test.
func SetHostname(t *testing.T, n string) {
	t.Helper()
	old := hostname
	hostname = func() (string, error) { return n, nil }
	t.Cleanup(func() { hostname = old })
}

// SetVerboseLog makes log the ssh -v log of every host, for the rest of
// the test.
func SetVerboseLog(t *testing.T, log []byte) {
	t.Helper()
	old := verboseLog
	verboseLog = func(context.Context, client.Host) ([]byte, error) { return log, nil }
	t.Cleanup(func() { verboseLog = old })
}

// AddEntry adds a file to a new archive, as the bundle does, and reports
// whether the archive took its name.
func AddEntry(t *testing.T, name string) error {
	t.Helper()
	a, err := newArchive(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer a.abort()
	return a.add(name, []byte("x"))
}

// ProfString is one string of a profile's table.
type ProfString struct {
	S    string
	File bool
}

// RewriteProfile is rewriteProfile, for the tests.
func RewriteProfile(data []byte, rewrite func(string) string) ([]byte, []ProfString, error) {
	out, table, err := rewriteProfile(data, rewrite)
	strs := make([]ProfString, len(table))
	for i, s := range table {
		strs[i] = ProfString{S: s.s, File: s.file}
	}
	return out, strs, err
}

// RewritePath is the rewrite of a profile's file names.
func (s *Scrubber) RewritePath(p string) string { return s.rewritePath(p) }

// HoldsHome is the check of a saved window.
func (s *Scrubber) HoldsHome(b []byte) bool { return s.holdsHome(b) }
