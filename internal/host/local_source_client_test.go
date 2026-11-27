//go:build linux

package host

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/client"
)

// Same-package host tests already use client SDK helpers. Installing from
// this test entrypoint avoids importing host back into production client.
func init() {
	if err := client.InstallLocalGitSources(func(ctx context.Context, path string, graph bool, environment []string) (client.LocalGitSource, error) {
		return OpenGitSourceCapability(ctx, path, graph)
	}, client.OpenLocalGitMetadataSource); err != nil {
		panic(fmt.Sprintf("install canonical host-test source provider: %v", err))
	}
}
