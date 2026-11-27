package egress

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
)

func physicalH2CandidateBuildInfo(tags string) *debug.BuildInfo {
	return &debug.BuildInfo{GoVersion: physicalH2QualifiedRuntime, Deps: []*debug.Module{{Path: "golang.org/x/net", Version: "v0.62.0", Sum: physicalH2QualifiedModuleSum}}, Settings: []debug.BuildSetting{{Key: "-tags", Value: tags}}}
}

func TestPhysicalH2CompatibilityRequiresExactHookedToolchainAndModule(t *testing.T) {
	actualInfo, actualInfoOK := debug.ReadBuildInfo()
	t.Logf("actual runtime=%q metadata_ok=%t compiled_http2legacy=%t", runtime.Version(), actualInfoOK, physicalH2LegacyServerCompiled)
	if actualInfoOK && actualInfo != nil {
		t.Logf("actual metadata GoVersion=%q Path=%q Main.Path=%q dependency_count=%d recorded_http2legacy=%t", actualInfo.GoVersion, actualInfo.Path, actualInfo.Main.Path, len(actualInfo.Deps), physicalH2LegacyTagRecorded(actualInfo.Settings))
		for _, setting := range actualInfo.Settings {
			if setting.Key == "-tags" {
				t.Logf("actual recorded tags=%q", setting.Value)
			}
		}
		for _, dependency := range actualInfo.Deps {
			if dependency != nil && dependency.Path == "golang.org/x/net" {
				t.Logf("actual x/net version=%q checksum=%q unreplaced=%t", dependency.Version, dependency.Sum, dependency.Replace == nil)
			}
		}
	}
	for _, tags := range []string{"http2legacy", "userns_cohort_v3,http2legacy", "http2legacy,userns_cohort_v3"} {
		if err := physicalH2CompatibilityError(physicalH2QualifiedRuntime, physicalH2CandidateBuildInfo(tags)); err != nil {
			t.Fatal("exact candidate metadata lost its legacy selection", err)
		}
	}
	if actualError := physicalH2HooksError(); actualError != nil || !physicalH2LegacyServerCompiled {
		info, ok := debug.ReadBuildInfo()
		dependencies := 0
		if ok && info != nil {
			dependencies = len(info.Deps)
		}
		t.Fatalf("the actual candidate build lacks qualified legacy hooks: runtime=%q compiled_legacy=%t metadata_ok=%t dependencies=%d actual_error=%v", runtime.Version(), physicalH2LegacyServerCompiled, ok, dependencies, actualError)
	}
	pinned := physicalH2CandidateBuildInfo("http2legacy")
	for _, version := range []string{"go1.26.11", "go1.27.3", "go1.27.5", "go1.28.0", "devel go1.27.4", ""} {
		if !errors.Is(physicalH2CompatibilityError(version, pinned), errPhysicalH2Toolchain) {
			t.Fatal("unqualified toolchain could run without its required hooks", version)
		}
	}
	for _, tags := range []string{"", "http2legacyish", "prefixhttp2legacy", "HTTP2LEGACY", "http2legacy ", "http2legacy,", "userns_cohort_v3"} {
		if !errors.Is(physicalH2CompatibilityError(physicalH2QualifiedRuntime, physicalH2CandidateBuildInfo(tags)), errPhysicalH2Toolchain) {
			t.Fatal("missing or lookalike recorded legacy tag was accepted", tags)
		}
	}
	for _, change := range []func(*debug.BuildInfo){
		func(info *debug.BuildInfo) { info.GoVersion = "" },
		func(info *debug.BuildInfo) { info.GoVersion = "go1.27.3" },
		func(info *debug.BuildInfo) { info.Deps = nil },
		func(info *debug.BuildInfo) { info.Deps = []*debug.Module{nil} },
		func(info *debug.BuildInfo) { info.Deps[0].Version = "v0.63.0" },
		func(info *debug.BuildInfo) { info.Deps[0].Sum = "" },
		func(info *debug.BuildInfo) { info.Deps[0].Sum = "h1:not-the-qualified-module" },
		func(info *debug.BuildInfo) { info.Deps[0].Replace = &debug.Module{Path: "local-copy"} },
		func(info *debug.BuildInfo) { info.Deps = append(info.Deps, info.Deps[0]) },
		func(info *debug.BuildInfo) { info.Settings = nil },
		func(info *debug.BuildInfo) { info.Settings = append(info.Settings, info.Settings[0]) },
	} {
		info := physicalH2CandidateBuildInfo("http2legacy")
		change(info)
		if !errors.Is(physicalH2CompatibilityError(physicalH2QualifiedRuntime, info), errPhysicalH2Toolchain) {
			t.Fatal("unknown, upgraded, replaced or ambiguous build metadata was accepted")
		}
	}
	if !errors.Is(physicalH2CompatibilityError(physicalH2QualifiedRuntime, nil), errPhysicalH2Toolchain) {
		t.Fatal("absent module metadata was accepted")
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
