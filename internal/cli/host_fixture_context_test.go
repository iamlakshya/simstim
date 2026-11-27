package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// saveHostContext preserves context-sensitive older fixtures. Manual edits
// use saveHost instead so they can race an advisory lock deliberately.
func saveHostContext(ctx context.Context, h client.Host) error {
	return client.SaveHostContext(ctx, h)
}

// writeFixtureHosts imitates a manual edit in the test's private config.
func writeFixtureHosts(hosts []client.Host) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Hosts for %s.\n\n", brand.DisplayName)
	if err := toml.NewEncoder(&b).Encode(struct {
		Hosts []client.Host `toml:"host"`
	}{Hosts: hosts}); err != nil {
		return fmt.Errorf("encode fixture hosts: %w", err)
	}
	if err := os.MkdirAll(client.ConfigDir(), 0700); err != nil {
		return fmt.Errorf("make fixture config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "hosts.toml"), b.Bytes(), 0600); err != nil {
		return fmt.Errorf("write fixture hosts: %w", err)
	}
	return nil
}
