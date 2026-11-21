//go:build linux

package gvisor

import (
	"context"
	"net"
	"testing"
)

func TestNativeGvisorLiveOwnerRejectsUnstartedOrCopiedFactoryState(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	for _, runtime := range []*sandbox{{}, {started: true, hello: true, connection: a}, {started: true, hello: true, connection: a, runtime: &Runtime{}}} {
		owner, err := NativeLiveOwnerV1(runtime, a)
		if err == nil || owner != nil || runtime.nativeLive != nil {
			t.Fatal("copied factory/hello data supplied native gvisor continuation")
		}
	}
	for _, owner := range []*LiveOwnerV1{nil, {}, {sandbox: &sandbox{}, runtime: &Runtime{}, connection: a}} {
		if err := owner.Check(context.Background()); err == nil {
			t.Fatal("copied gvisor data supplied native readiness")
		}
	}
}
