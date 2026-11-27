//go:build !windows

package client

import "testing"

func stateFDPrivatePath(t *testing.T, path string) { t.Helper() }
