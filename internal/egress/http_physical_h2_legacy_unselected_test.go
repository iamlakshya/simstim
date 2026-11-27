//go:build !http2legacy

package egress

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
)

// This is a real no-tag artifact check, not a metadata override or a skip.
func TestPhysicalH2ActualMissingLegacyTagRefusesBeforeInterception(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if runtime.Version() != physicalH2QualifiedRuntime || !ok || info == nil || info.GoVersion != physicalH2QualifiedRuntime || physicalH2LegacyServerCompiled || physicalH2LegacyTagRecorded(info.Settings) {
		t.Fatal("no-tag rejection artifact did not retain its actual candidate identity")
	}
	modules := 0
	for _, dependency := range info.Deps {
		if dependency != nil && dependency.Path == "golang.org/x/net" {
			if dependency.Version != "v0.62.0" || dependency.Sum != physicalH2QualifiedModuleSum || dependency.Replace != nil {
				t.Fatal("no-tag artifact lacks the actual pinned unreplaced module")
			}
			modules++
		}
	}
	if modules != 1 || !errors.Is(physicalH2HooksError(), errPhysicalH2Toolchain) {
		t.Fatal("actual missing legacy selection did not refuse admission")
	}
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	observed := &physicalH2UntouchedConn{Conn: local}
	actual := tls.Server(observed, &tls.Config{MinVersion: tls.VersionTLS12})
	var handled atomic.Int64
	if err := servePhysicalHTTP2(t.Context(), actual, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handled.Add(1) })); !errors.Is(err, errPhysicalH2Toolchain) {
		t.Fatal("actual no-tag server did not refuse before TLS", err)
	}
	server := newMemberServer(t, InitArgs{Agent: "shell"})
	entry := server.serveIntercept(t.Context(), observed, observed, StreamHeader{Host: "api.openai.com", Port: 443}, true)
	if entry.Decision != "deny" || entry.Rule != "http2:toolchain" || !strings.Contains(entry.Reason, "Try:") || !strings.Contains(entry.Reason, "http2legacy") || observed.reads.Load() != 0 || observed.writes.Load() != 0 || handled.Load() != 0 || len(server.flows.snapshot()) != 0 || len(server.httpSem) != 0 {
		t.Fatal("missing compiled legacy selection inspected credentials, fell back or lost its build hint", entry)
	}
}
