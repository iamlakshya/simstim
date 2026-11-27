//go:build linux

package guest

import (
	"net"

	"github.com/iamlakshya/simstim/internal/link"
)

// holderStream tracks only the actual link and fixed member Unix socket that
// the ONE ordinary guest bridge opened. Stop closes both directions and joins
// its existing pumps before capture/unmount; no replacement IO engine exists.
func (v *vmRuntime) holderStream(stream *link.Stream, conn net.Conn) (func(), error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ctx.Err() != nil {
		return nil, vmError("the VM stopped before the holder stream began")
	}
	if v.holderIO == nil {
		v.holderIO = map[*link.Stream]net.Conn{}
	}
	v.holderIO[stream] = conn
	v.holderWG.Add(1)
	return func() { v.mu.Lock(); delete(v.holderIO, stream); v.mu.Unlock(); v.holderWG.Done() }, nil
}
