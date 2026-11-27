//go:build !linux

package join

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/join/code"
	"io"
)

// ApplyLinux requires Linux; other machine backends own their own native flow.
func ApplyLinux(context.Context, *LinuxPlan, io.Writer) (code.Reply, error) {
	return code.Reply{}, codeFailure("join_platform", "this join backend needs Linux")
}

// LeaveLinux requires Linux and its original root-owned ledger.
func LeaveLinux(context.Context, string, io.Writer) error {
	return codeFailure("join_platform", "this leave backend needs Linux")
}

// ProbeCurrentAccount is the Linux account-scoped child entry.
func ProbeCurrentAccount(context.Context) (api.CapProbe, error) {
	return api.CapProbe{}, codeFailure("join_platform", "this probe backend needs Linux")
}
