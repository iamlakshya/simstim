package guest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/pkgsettings.golden")

func TestBuildPackageSettingsGolden(t *testing.T) {
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	levels := []string{api.PackageLevelGuarded, api.PackageLevelStrict, api.PackageLevelOff}
	ages := []string{"0d", "1d", "7d"}

	var out strings.Builder
	for _, lvl := range levels {
		for _, age := range ages {
			plan, err := BuildPackageSettings(PackageSettingsConfig{
				Level:         lvl,
				MinReleaseAge: age,
				Now:           fixedNow,
			})
			if err != nil {
				t.Fatalf("BuildPackageSettings(%s, %s): %v", lvl, age, err)
			}
			fmt.Fprintf(&out, "=== level=%s age=%s ===\n", lvl, age)
			if len(plan.StripEnv) > 0 {
				fmt.Fprintf(&out, "strip_env: %s\n", strings.Join(plan.StripEnv, ","))
			}
			envKeys := make([]string, 0, len(plan.Env))
			for k := range plan.Env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for _, k := range envKeys {
				fmt.Fprintf(&out, "env %s=%s\n", k, plan.Env[k])
			}
			fileKeys := make([]string, 0, len(plan.Files))
			for k := range plan.Files {
				fileKeys = append(fileKeys, k)
			}
			sort.Strings(fileKeys)
			for _, k := range fileKeys {
				fmt.Fprintf(&out, "--- file %s ---\n%s", k, plan.Files[k])
			}
			out.WriteByte('\n')
		}
	}

	goldenPath := filepath.Join("testdata", "pkgsettings.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v (run go test ./internal/guest -update-golden to generate)", goldenPath, err)
	}
	if out.String() != string(wantBytes) {
		t.Fatalf("package settings golden mismatch.\n--- got ---\n%s\n--- want ---\n%s", out.String(), string(wantBytes))
	}
}

func TestApplyPackageSettingsFilesOverridesAndOff(t *testing.T) {
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	t.Run("guarded writes home files strips Go bypass vars and lets sandbox.env win", func(t *testing.T) {
		home := t.TempDir()
		base := []string{
			"HOME=" + home,
			"PATH=/usr/bin:/bin",
			"GOFLAGS=-mod=vendor",
			"GOPRIVATE=should.be.stripped",
			"GONOPROXY=should.be.stripped",
			"GONOSUMDB=should.be.stripped",
			"GOINSECURE=should.be.stripped",
		}
		userEnv := map[string]string{
			"npm_config_ignore_scripts": "false",
			"GOPROXY":                   "https://corp.example/go",
			"GOPRIVATE":                 "corp.example/*",
		}
		got, err := ApplyPackageSettings(home, base, userEnv, PackageSettingsConfig{
			Level:         api.PackageLevelGuarded,
			MinReleaseAge: "7d",
			Now:           fixedNow,
		})
		if err != nil {
			t.Fatalf("ApplyPackageSettings: %v", err)
		}
		envMap := parseEnvSlice(got)
		if envMap["npm_config_ignore_scripts"] != "false" {
			t.Errorf("sandbox.env override failed: npm_config_ignore_scripts = %q, want false", envMap["npm_config_ignore_scripts"])
		}
		if envMap["npm_config_min_release_age"] != "7" {
			t.Errorf("npm_config_min_release_age = %q, want 7", envMap["npm_config_min_release_age"])
		}
		if envMap["GOPROXY"] != "https://corp.example/go" {
			t.Errorf("GOPROXY = %q, want user override", envMap["GOPROXY"])
		}
		if envMap["GOPRIVATE"] != "corp.example/*" {
			t.Errorf("GOPRIVATE = %q, want user override", envMap["GOPRIVATE"])
		}
		for _, stripped := range []string{"GOFLAGS", "GONOPROXY", "GONOSUMDB", "GOINSECURE"} {
			if _, ok := envMap[stripped]; ok {
				t.Errorf("%s should be stripped, got %q", stripped, envMap[stripped])
			}
		}
		if envMap["GOTOOLCHAIN"] != "local" || envMap["GOVCS"] != "*:off" || envMap["GOSUMDB"] != "sum.golang.org" {
			t.Errorf("Go vars = (%q, %q, %q)", envMap["GOTOOLCHAIN"], envMap["GOVCS"], envMap["GOSUMDB"])
		}
		if _, ok := envMap["PIP_ONLY_BINARY"]; ok {
			t.Error("PIP_ONLY_BINARY must not be set in guarded level")
		}
		if _, ok := envMap["UV_NO_BUILD"]; ok {
			t.Error("UV_NO_BUILD must not be set in guarded level")
		}

		pnpmCfg, err := os.ReadFile(filepath.Join(home, ".config", "pnpm", "config.yaml"))
		if err != nil {
			t.Fatalf("read pnpm config.yaml: %v", err)
		}
		if string(pnpmCfg) != "ignoreScripts: true\nminimumReleaseAge: 10080\n" {
			t.Errorf("pnpm config.yaml = %q", string(pnpmCfg))
		}
		bunCfg, err := os.ReadFile(filepath.Join(home, ".bunfig.toml"))
		if err != nil {
			t.Fatalf("read .bunfig.toml: %v", err)
		}
		if string(bunCfg) != "[install]\nignoreScripts = true\nminimumReleaseAge = 604800\n" {
			t.Errorf(".bunfig.toml = %q", string(bunCfg))
		}
	})

	t.Run("strict adds pip and uv no-build settings", func(t *testing.T) {
		home := t.TempDir()
		got, err := ApplyPackageSettings(home, []string{"HOME=" + home}, nil, PackageSettingsConfig{
			Level:         api.PackageLevelStrict,
			MinReleaseAge: "7d",
			Now:           fixedNow,
		})
		if err != nil {
			t.Fatalf("ApplyPackageSettings(strict): %v", err)
		}
		envMap := parseEnvSlice(got)
		if envMap["PIP_ONLY_BINARY"] != ":all:" {
			t.Errorf("PIP_ONLY_BINARY = %q, want :all:", envMap["PIP_ONLY_BINARY"])
		}
		if envMap["UV_NO_BUILD"] != "1" {
			t.Errorf("UV_NO_BUILD = %q, want 1", envMap["UV_NO_BUILD"])
		}
	})

	t.Run("off writes no files and no guard variables", func(t *testing.T) {
		home := t.TempDir()
		got, err := ApplyPackageSettings(home, []string{"HOME=" + home, "PATH=/usr/bin"}, map[string]string{"FOO": "bar"}, PackageSettingsConfig{
			Level:         api.PackageLevelOff,
			MinReleaseAge: "7d",
			Now:           fixedNow,
		})
		if err != nil {
			t.Fatalf("ApplyPackageSettings(off): %v", err)
		}
		envMap := parseEnvSlice(got)
		for _, absent := range []string{
			"npm_config_ignore_scripts", "npm_config_min_release_age",
			"YARN_ENABLE_SCRIPTS", "YARN_NPM_MINIMAL_AGE_GATE",
			"PIP_UPLOADED_PRIOR_TO", "PIP_ONLY_BINARY",
			"UV_EXCLUDE_NEWER", "UV_NO_BUILD",
			"GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOVCS",
		} {
			if _, ok := envMap[absent]; ok {
				t.Errorf("off level must not set %s", absent)
			}
		}
		if envMap["FOO"] != "bar" {
			t.Errorf("sandbox.env FOO = %q, want bar", envMap["FOO"])
		}
		entries, err := os.ReadDir(home)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("off level wrote unexpected files in home: %v", entries)
		}
	})

	t.Run("invalid level and age rejected", func(t *testing.T) {
		if _, err := BuildPackageSettings(PackageSettingsConfig{Level: "on"}); err == nil {
			t.Error("expected error for invalid package level")
		}
		if _, err := BuildPackageSettings(PackageSettingsConfig{Level: "guarded", MinReleaseAge: "90m"}); err == nil {
			t.Error("expected error for invalid min_release_age")
		}
	})
}

func TestProxyEnvIncludesPackageManagerVars(t *testing.T) {
	env := ProxyEnv("http://127.0.0.1:3128", agents.Agent{})
	for _, k := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy",
		"npm_config_proxy", "npm_config_https_proxy",
		"CARGO_HTTP_PROXY",
	} {
		if env[k] != "http://127.0.0.1:3128" {
			t.Errorf("ProxyEnv[%s] = %q, want http://127.0.0.1:3128", k, env[k])
		}
	}
}

func TestGuestProxyDefaultPolicyAllowsPackageRegistries(t *testing.T) {
	registries := []string{
		"registry.npmjs.org",
		"pypi.org",
		"files.pythonhosted.org",
		"proxy.golang.org",
		"sum.golang.org",
		"crates.io",
		"index.crates.io",
		"static.crates.io",
	}
	allHosts := append(append([]string(nil), registries...), "evil.example")

	// Local TLS upstream valid for all test hostnames.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "registries.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     allHosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	pool.AddCert(parsed)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok:"+r.Host)
	}))
	up.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	up.StartTLS()
	defer up.Close()

	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
	res := staticResolver{}
	for _, h := range allHosts {
		res[h] = []netip.Addr{fakePub}
	}
	sd := &egress.SafeDialer{
		Addrs:    ap,
		Resolver: res,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, up.Listener.Addr().String())
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	supConn, egConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- egress.RunOnConn(ctx, "pkg-registries", egConn, &egress.ServerOptions{
			Addrs:  ap,
			Dialer: sd,
		})
	}()
	// Empty policy uses the default preset ("balanced").
	cli, err := egress.NewClient(ctx, supConn, egress.StartArgs{
		Session: "box/pkg1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-done
	}()

	gp, err := ListenProxy(ctx, cli, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go gp.Serve(ctx)
	defer func() { _ = gp.Close() }()

	proxyURL, _ := url.Parse("http://" + gp.Addr().String())
	hc := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 5 * time.Second,
	}

	for _, host := range registries {
		resp, err := hc.Get("https://" + host + "/")
		if err != nil {
			t.Fatalf("GET https://%s/ via guest.Proxy failed: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ok:"+host {
			t.Errorf("https://%s/: status=%d body=%q", host, resp.StatusCode, body)
		}
	}

	// Unlisted host must be refused with 403 by the default policy.
	if resp, err := hc.Get("https://evil.example/"); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("https://evil.example/ unexpectedly succeeded with status %d", resp.StatusCode)
	} else if !strings.Contains(err.Error(), "Forbidden") && !strings.Contains(err.Error(), "403") {
		t.Errorf("https://evil.example/ error = %v, want 403 Forbidden", err)
	}
}
