package egress

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"

	"github.com/iamlakshya/simstim/internal/api"
)

// Public ServeConn consumes this already established native TLS state to
// preserve its original HTTP2 TLS/cipher checks. It never handshakes this
// view. The underlying opaque plaintext observer still exposes no TLS state
// to the separate standard UnencryptedHTTP2 dispatcher used by old proofs.
type physicalH2NativeTLSView struct {
	net.Conn
	owner *physicalH2Owner
}

func (c *physicalH2NativeTLSView) ConnectionState() tls.ConnectionState { return c.owner.tlsState }
func (*physicalH2NativeTLSView) Format(f fmt.State, _ rune)             { _, _ = io.WriteString(f, api.Redacted) }
