package guest

import (
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// applyWallLandlock applies the process-wall Landlock ruleset if passed via
// environment, before wall-init attaches to fd 3 or reads any link frames
// (reuse.md Part A §1).
func applyWallLandlock() error {
	cfg, ok, err := process.LandlockFromEnv()
	if !ok {
		return nil
	}
	if err != nil {
		return err
	}
	envVar := process.LandlockEnvVar()
	_ = os.Unsetenv(envVar)
	if err := process.ApplyLandlock(cfg); err != nil {
		return err
	}
	prefix := envVar + "="
	cleanEnv := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, prefix) {
			cleanEnv = append(cleanEnv, kv)
		}
	}
	return reexecAfterLandlock(cleanEnv)
}

func guestHolderEnv(sock, hookSock, manifestPath, logPath string) []string {
	env := []string{
		brand.Env("GUEST_SOCKET") + "=" + sock,
		brand.Env("GUEST_HOOK_SOCKET") + "=" + hookSock,
		brand.Env("GUEST_MANIFEST") + "=" + manifestPath,
		brand.Env("GUEST_LOG") + "=" + logPath,
		brand.Env("GUEST_EVENTS_FD") + "=3",
	}
	for _, k := range []string{"PATH", "HOME", "GORACE", "GOCOVERDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func withEnvVar(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+val)
}

// copyOneWay copies src into dst and half-closes dst's write side if supported.
func copyOneWay(dst io.Writer, src io.Reader) {
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
