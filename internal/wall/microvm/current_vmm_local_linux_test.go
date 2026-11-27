//go:build linux

package microvm

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestLocalCurrentVMMCannotOriginateFromCopiedOrAbsentBinding(t *testing.T) {
	called := false
	for _, binding := range []*NativeBindingV1{nil, {}} {
		err := binding.WithLocalCurrentCopyVMMV1(context.Background(), nil, vmneed.NeedV1{}, func(LocalCurrentCopyVMMV1) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("an unissued binding created local VMM evidence")
		}
	}
	for _, loan := range []*localCurrentVMMV1{nil, {}} {
		err := loan.WithVMMFilesV1(context.Background(), func(*os.File, *os.File) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("an absent original VMM loan supplied a native actor")
		}
	}
}
