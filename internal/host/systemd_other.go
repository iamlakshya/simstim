//go:build !linux

package host

import "context"

func stopUnitViaDBus(context.Context, string) bool { return false }
