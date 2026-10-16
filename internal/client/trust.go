package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// The user's answers to "may this repository's setup script run?", kept by
// host and repository in the client's state, so each repository is asked
// about once on each host.

// SetupQuestion asks whether a repository's setup script may run.
type SetupQuestion = api.SetupQuestion

type trustFile struct {
	// Hosts maps a host's name to its repositories and whether their setup
	// scripts may run.
	Hosts map[string]map[string]bool `json:"hosts"`
}

var trustMu sync.Mutex

func trustPath() string { return filepath.Join(StateDir(), "setup-trust.json") }

func loadTrust() trustFile {
	var f trustFile
	if b, err := os.ReadFile(trustPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	if f.Hosts == nil {
		f.Hosts = map[string]map[string]bool{}
	}
	return f
}

// SetupTrust returns the user's answers for a host: repository → may run.
func SetupTrust(host string) map[string]bool {
	trustMu.Lock()
	defer trustMu.Unlock()
	return loadTrust().Hosts[host]
}

// RememberSetup keeps the user's answer for a repository on a host.
func RememberSetup(host, repo string, run bool) error {
	trustMu.Lock()
	defer trustMu.Unlock()
	f := loadTrust()
	if f.Hosts[host] == nil {
		f.Hosts[host] = map[string]bool{}
	}
	f.Hosts[host][repo] = run
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("save the setup answers: %w", err)
	}
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", StateDir(), err)
	}
	tmp := trustPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, trustPath()); err != nil {
		return fmt.Errorf("save the answer: %w", err)
	}
	return nil
}
