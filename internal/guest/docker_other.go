//go:build !unix

package guest

import (
	"context"
	"errors"
	"time"
)

type dockerOptions struct {
	DockerdPath string
	PublicSock  string
	DaemonSock  string
	DataRoot    string
	ExecRoot    string
	PIDFile     string
	DockerGID   int
	ProxyURL    string
	StartWait   time.Duration
}

type dockerManager struct{}

func findDockerd(...string) string { return "" }

func startDockerActivator(context.Context, dockerOptions) (*dockerManager, error) {
	return nil, errors.New("docker inside microvm is only supported on Linux")
}

func (*dockerManager) Stop(time.Duration) {}
