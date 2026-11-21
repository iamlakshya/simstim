package egress

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPhysicalH2CompatibilityRequiresExactHookedToolchainAndModule(t *testing.T) {
	pinned := &debug.BuildInfo{Deps: []*debug.Module{{Path: "golang.org/x/net", Version: "v0.62.0"}}}
	if err := physicalH2CompatibilityError("go1.26.11", pinned); err != nil || physicalH2HooksError() != nil {
		t.Fatal("the actual pinned build did not retain its qualified legacy hooks", err)
	}
	for _, version := range []string{"go1.26.10", "go1.26.12", "go1.27.0", "devel go1.26.11", ""} {
		if !errors.Is(physicalH2CompatibilityError(version, pinned), errPhysicalH2Toolchain) {
			t.Fatal("unqualified toolchain could run without its required hooks", version)
		}
	}
	for _, info := range []*debug.BuildInfo{nil, {}, {Deps: []*debug.Module{{Path: "golang.org/x/net", Version: "v0.63.0"}}}, {Deps: []*debug.Module{{Path: "golang.org/x/net", Version: "v0.62.0", Replace: &debug.Module{Path: "local-copy"}}}}} {
		if !errors.Is(physicalH2CompatibilityError("go1.26.11", info), errPhysicalH2Toolchain) {
			t.Fatal("missing, upgraded or replaced scheduler dependency was accepted")
		}
	}
}

type physicalH2UntouchedConn struct {
	net.Conn
	reads, writes atomic.Int64
}

func (c *physicalH2UntouchedConn) Read([]byte) (int, error) {
	c.reads.Add(1)
	return 0, io.ErrUnexpectedEOF
}
func (c *physicalH2UntouchedConn) Write([]byte) (int, error) {
	c.writes.Add(1)
	return 0, io.ErrUnexpectedEOF
}

func TestPhysicalH2MissingHooksRefuseBeforeTLSHTTPOrCredentialInspection(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	observed := &physicalH2UntouchedConn{Conn: local}
	var handled atomic.Int64
	actual := tls.Server(observed, &tls.Config{MinVersion: tls.VersionTLS12})
	if err := servePhysicalHTTP2WithHooks(t.Context(), actual, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handled.Add(1) }), errPhysicalH2Toolchain); !errors.Is(err, errPhysicalH2Toolchain) {
		t.Fatal("unsupported hooks did not return the fixed actionable error", err)
	}
	server := newMemberServer(t, InitArgs{Agent: "shell"})
	entry := server.serveInterceptWithH2Hooks(t.Context(), observed, observed, StreamHeader{Host: "api.openai.com", Port: 443}, true, errPhysicalH2Toolchain)
	if entry.Decision != "deny" || entry.Rule != "http2:toolchain" || !strings.Contains(entry.Reason, "Try:") || observed.reads.Load() != 0 || observed.writes.Load() != 0 || handled.Load() != 0 || len(server.flows.snapshot()) != 0 || len(server.httpSem) != 0 {
		t.Fatal("hook-less build inspected credentials, fell back, or lost its actionable refusal", entry)
	}
}
