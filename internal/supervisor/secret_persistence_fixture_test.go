package supervisor

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/egress"
)

const caBoundaryMode = "TEST_EGRESS_CA_BOUNDARY"

// This test-child mode uses the actual fd3 entry and private authority. Its
// deny-all origin hooks make the existing host-file persistence oracle local.
func caBoundaryEgressChild() bool {
	if os.Getenv(caBoundaryMode) != "1" || len(os.Args) < 2 || os.Args[1] != "egress" {
		return false
	}
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "invalid owned egress fixture arguments")
		os.Exit(1)
	}
	options := egress.Options{OriginRoots: x509.NewCertPool(), Dialer: &egress.Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("CA boundary fixture permits no DNS")
		},
		DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
			return nil, errors.New("CA boundary fixture permits no origin connection")
		},
	}}
	if err := egress.RunWithOptions(os.Args[2], options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

func caBoundaryEnvironment() []string {
	env := []string{caBoundaryMode + "=1", "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_DATA_DIRS", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "GNUPGHOME", "TMPDIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GORACE", "GOCOVERDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func caBoundaryFolders(t *testing.T) string {
	t.Helper()
	dir := testFolders(t)
	for key, relative := range map[string]string{"USERPROFILE": "home", "XDG_CACHE_HOME": "cache", "XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_DIRS": "data-dirs", "GNUPGHOME": "gnupg", "TMPDIR": "tmp"} {
		path := filepath.Join(dir, relative)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return dir
}
