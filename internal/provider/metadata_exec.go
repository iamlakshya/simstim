package provider

import (
	"bytes"
	"context"
	"fmt"
)

type boundedMetadataOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedMetadataOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.exceeded = true
		return 0, fmt.Errorf("provider metadata output exceeds its bound")
	}
	return b.Buffer.Write(data)
}

// ExecMetadata uses the SAME provider executor with explicit checked metadata,
// a fixed local inspector tool and bounded output. It never imports a caller's
// ambient environment or current-directory/PATHEXT executable fallback.
func ExecMetadata(ctx context.Context, environment []string, limit int, install, name string, args ...string) ([]byte, error) {
	if environment == nil || limit <= 0 || name != "docker" && name != "limactl" {
		return nil, fmt.Errorf("provider metadata operation has no closed environment")
	}
	resolved, err := resolveProviderEnvironment(name, environment)
	if err != nil {
		return nil, err
	}
	return execInput(ctx, nil, environment, limit, install, resolved, args...)
}
