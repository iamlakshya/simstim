//go:build !darwin

package join

import (
	"context"
	"github.com/iamlakshya/simstim/internal/join/code"
	"io"
)

// ApplyDarwin has no native Mac source on another operating system.
func ApplyDarwin(context.Context, *DarwinPlan, io.Writer) (code.Reply, error) {
	return code.Reply{}, macFailure("join_mac_platform", "Mac enrolment must run on the Mac's own keyboard")
}

// LeaveDarwin never interprets a Mac root ledger on a different platform.
func LeaveDarwin(context.Context, string, io.Writer) error {
	return macFailure("join_mac_platform", "Mac leave must run through its original Mac root helper")
}

// ResolveDarwinOptions cannot invent native Mac dimensions on another platform.
func ResolveDarwinOptions(_ context.Context, options DarwinOptions) (DarwinOptions, error) {
	return options, macFailure("join_mac_platform", "Mac VM defaults need the actual Mac hardware")
}
