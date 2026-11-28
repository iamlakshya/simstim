package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

const (
	// maxClientRuntimeAssetBytes caps any single runtime artefact downloaded on
	// the laptop before copying to a host.
	maxClientRuntimeAssetBytes = 2 << 30 // 2 GiB
	defaultRuntimePollInterval = time.Second
)

// defaultRuntimeManifest returns the embedded release manifest for this client.
// Until non-embedded runtime items (VMM, kernel, tools image, runsc) ship in a
// release, it is empty. Tests replace it to verify delivery paths.
var defaultRuntimeManifest = func() api.RuntimeManifest {
	return api.RuntimeManifest{}
}

// DefaultRuntimeManifest returns the runtime manifest embedded in this client.
func DefaultRuntimeManifest() api.RuntimeManifest {
	return defaultRuntimeManifest()
}

func (c *Conn) needRuntime() error {
	if c.HasFeature("runtime") {
		return nil
	}
	return &UserError{
		Msg:  "the helper on " + c.Host.Name + " is too old for runtime delivery",
		Hint: brand.Name + " host upgrade " + ShellQuote(c.Host.Name),
		Code: "wall_helper_old",
	}
}

// RuntimeStatus asks the host for its installed runtime labels, cached runtime
// files and active runtime.fetch downloads (isolation spec 14.9.3).
func (c *Conn) RuntimeStatus(ctx context.Context) (api.RuntimeStatusResult, error) {
	if err := c.needRuntime(); err != nil {
		return api.RuntimeStatusResult{}, err
	}
	var out api.RuntimeStatusResult
	err := c.Call(ctx, "runtime.status", nil, &out)
	return out, err
}

// RuntimeFetch asks the host to download the named runtime assets directly
// over HTTPS and verify each against the client's SHA-256 and byte size.
func (c *Conn) RuntimeFetch(ctx context.Context, args api.RuntimeFetchArgs) (api.RuntimeInstallResult, error) {
	if err := c.needRuntime(); err != nil {
		return api.RuntimeInstallResult{}, err
	}
	var out api.RuntimeInstallResult
	err := c.Call(ctx, "runtime.fetch", args, &out)
	return out, err
}

// RuntimeInstall asks the host to verify uploaded part files in
// artefacts/incoming/ and move them into artefacts/sha256/.
func (c *Conn) RuntimeInstall(ctx context.Context, args api.RuntimeInstallArgs) (api.RuntimeInstallResult, error) {
	if err := c.needRuntime(); err != nil {
		return api.RuntimeInstallResult{}, err
	}
	var out api.RuntimeInstallResult
	err := c.Call(ctx, "runtime.install", args, &out)
	return out, err
}

// EnsureRuntimeOptions configures EnsureRuntime.
type EnsureRuntimeOptions struct {
	// Mode is "auto", "host" or "laptop". Empty reads [images] fetch from
	// config.toml (default "auto").
	Mode string
	// Manifest is the client's release manifest. When empty,
	// DefaultRuntimeManifest is used.
	Manifest api.RuntimeManifest
	// Arch is the host's architecture ("amd64" or "arm64").
	Arch string
	// Probe, when provided, avoids a second sandbox.probe call in "auto" mode.
	Probe *api.CapProbe
	// Transport is used to copy files from the laptop cache into
	// artefacts/incoming/ in "laptop" mode (or on fallback from "host"). When
	// nil, EnsureRuntime opens the host's transport.
	Transport Transport
	// Interactive controls SSH interactivity if EnsureRuntime opens Transport.
	Interactive bool
	// Out receives per-file progress lines and fallback notices. Nil is quiet.
	Out io.Writer
	// PollInterval controls how often "host" mode polls runtime.status for
	// live bytes. Zero defaults to 1 second (spec 14.9.3).
	PollInterval time.Duration
	// HTTPClient performs laptop downloads in "laptop" mode. Nil uses an
	// HTTPS-only client with a bounded timeout.
	HTTPClient *http.Client
	// CacheDir overrides RuntimeCacheDir for laptop downloads.
	CacheDir string
}

// EnsureRuntimeResult summarises one EnsureRuntime run.
type EnsureRuntimeResult struct {
	Mode           string                     `json:"mode,omitempty"`
	Runtime        string                     `json:"runtime,omitempty"`
	Installed      []api.RuntimeInstalledFile `json:"installed,omitempty"`
	AlreadyPresent int                        `json:"already_present,omitempty"`
	FellBack       bool                       `json:"fell_back,omitempty"`
	FallbackReason string                     `json:"fallback_reason,omitempty"`
}

// EnsureRuntime makes sure the host holds every runtime artefact in the release
// manifest for opts.Arch, following isolation spec 14.9.3:
//   - "host": host downloads missing files via runtime.fetch while client polls
//     runtime.status once a second.
//   - "laptop": client downloads into ~/.cache/<name>/runtime/sha256/, checks
//     size and SHA-256, copies each into artefacts/incoming/<hex>.<rand>.part
//     over the host transport, and calls runtime.install.
//   - "auto": "host" when sandbox.probe reports release_reachable, else
//     "laptop"; if the host download fails, falls back to "laptop" once and
//     says so.
//
// No host ever receives a runtime file from another host, or any credential for
// another host.
func EnsureRuntime(ctx context.Context, conn *Conn, opts EnsureRuntimeOptions) (EnsureRuntimeResult, error) {
	modeRaw := opts.Mode
	if strings.TrimSpace(modeRaw) == "" {
		modeRaw = LoadSettings().Images.Fetch
	}
	mode, err := api.ParseImagesFetch(modeRaw)
	if err != nil {
		return EnsureRuntimeResult{}, &UserError{
			Msg:  err.Error(),
			Hint: "Use auto, host or laptop.",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}

	manifest := opts.Manifest
	if len(manifest.Items) == 0 && manifest.Runtime == "" {
		manifest = DefaultRuntimeManifest()
	}
	items := manifest.ItemsForArch(opts.Arch)
	if len(items) == 0 {
		return EnsureRuntimeResult{Mode: mode, Runtime: manifest.Runtime}, nil
	}
	if err := conn.needRuntime(); err != nil {
		return EnsureRuntimeResult{}, err
	}

	normItems, err := normalizeRuntimeItems(items)
	if err != nil {
		return EnsureRuntimeResult{}, err
	}

	missing, already, err := findMissingRuntimeAssets(ctx, conn, manifest, normItems)
	if err != nil {
		return EnsureRuntimeResult{}, err
	}
	if len(missing) == 0 {
		return EnsureRuntimeResult{
			Mode:           mode,
			Runtime:        manifest.Runtime,
			AlreadyPresent: already,
		}, nil
	}

	delivery := pickRuntimeDeliveryMode(ctx, conn, mode, opts, missing[0].URL)
	if delivery == api.ImagesFetchHost {
		return ensureViaHostWithFallback(ctx, conn, mode, manifest, missing, normItems, already, opts)
	}
	res, err := deliverRuntimeViaLaptop(ctx, conn, manifest, missing, normItems, opts)
	if err != nil {
		return EnsureRuntimeResult{}, err
	}
	res.AlreadyPresent = already
	return res, nil
}

func findMissingRuntimeAssets(ctx context.Context, conn *Conn, manifest api.RuntimeManifest, normItems []api.RuntimeAsset) ([]api.RuntimeAsset, int, error) {
	status, err := conn.RuntimeStatus(ctx)
	if err != nil {
		return nil, 0, err
	}
	var missing []api.RuntimeAsset
	already := 0
	for _, it := range normItems {
		if sz, ok := status.Files[it.SHA256]; ok && sz == it.Size {
			already++
			continue
		}
		missing = append(missing, it)
	}
	if len(missing) == 0 && manifest.Runtime != "" && !slices.Contains(status.Runtimes, manifest.Runtime) {
		if _, err := conn.RuntimeInstall(ctx, api.RuntimeInstallArgs{
			Release:    manifest.Release,
			Runtime:    manifest.Runtime,
			LabelItems: normItems,
		}); err != nil {
			return nil, 0, err
		}
	}
	return missing, already, nil
}

func pickRuntimeDeliveryMode(ctx context.Context, conn *Conn, mode string, opts EnsureRuntimeOptions, probeURL string) string {
	if mode != api.ImagesFetchAuto {
		return mode
	}
	if probeReleaseReachableForHost(ctx, conn, opts.Probe, probeURL) {
		return api.ImagesFetchHost
	}
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "  release is not reachable from %s; copying runtime from this computer\n", conn.Host.Name)
	}
	return api.ImagesFetchLaptop
}

func ensureViaHostWithFallback(ctx context.Context, conn *Conn, mode string, manifest api.RuntimeManifest, missing, allItems []api.RuntimeAsset, already int, opts EnsureRuntimeOptions) (EnsureRuntimeResult, error) {
	res, ferr := deliverRuntimeViaHost(ctx, conn, manifest, missing, allItems, opts)
	if ferr == nil {
		res.AlreadyPresent = already
		return res, nil
	}
	if mode != api.ImagesFetchAuto || ctx.Err() != nil {
		return EnsureRuntimeResult{}, ferr
	}
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "  ! host download failed (%s); falling back to laptop copy\n", CleanLine(ferr.Error()))
	}
	res, lerr := deliverRuntimeViaLaptop(ctx, conn, manifest, missing, allItems, opts)
	if lerr != nil {
		return EnsureRuntimeResult{}, lerr
	}
	res.AlreadyPresent = already
	res.FellBack = true
	res.FallbackReason = ferr.Error()
	return res, nil
}

func normalizeRuntimeItems(items []api.RuntimeAsset) ([]api.RuntimeAsset, error) {
	norm := make([]api.RuntimeAsset, 0, len(items))
	for _, it := range items {
		normHex, ok := api.NormalizeSHA256Hex(it.SHA256)
		if !ok {
			return nil, &UserError{
				Msg:  fmt.Sprintf("invalid SHA-256 digest in release manifest for %q", it.Name),
				Hint: "Update the client.",
				Code: "digest_mismatch",
			}
		}
		if it.Size <= 0 || it.Size > maxClientRuntimeAssetBytes {
			return nil, &UserError{
				Msg:  fmt.Sprintf("invalid size %d in release manifest for %q", it.Size, it.Name),
				Hint: "Update the client.",
				Code: "runtime_too_big",
			}
		}
		it.SHA256 = normHex
		norm = append(norm, it)
	}
	return norm, nil
}

func probeReleaseReachableForHost(ctx context.Context, conn *Conn, probe *api.CapProbe, fallbackURL string) bool {
	if probe != nil && probe.ReleaseReachable != nil {
		return *probe.ReleaseReachable
	}
	u := strings.TrimSpace(fallbackURL)
	if u == "" {
		u = ReleaseURL()
	}
	if u == "" || !conn.HasFeature("sandbox") {
		return false
	}
	p, err := conn.CapProbe(ctx, u)
	if err != nil || p.ReleaseReachable == nil {
		return false
	}
	return *p.ReleaseReachable
}

func deliverRuntimeViaHost(
	ctx context.Context,
	conn *Conn,
	manifest api.RuntimeManifest,
	missing, allItems []api.RuntimeAsset,
	opts EnsureRuntimeOptions,
) (EnsureRuntimeResult, error) {
	pollEvery := opts.PollInterval
	if pollEvery <= 0 {
		pollEvery = defaultRuntimePollInterval
	}

	pollCtx, stopPoll := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(pollEvery)
		defer ticker.Stop()
		lastBytes := map[string]int64{}
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				reqCtx, cancel := context.WithTimeout(pollCtx, 2*time.Second)
				st, err := conn.RuntimeStatus(reqCtx)
				cancel()
				if err != nil || opts.Out == nil {
					continue
				}
				for _, tr := range st.Active {
					if tr.Bytes > lastBytes[tr.SHA256] {
						lastBytes[tr.SHA256] = tr.Bytes
						fmt.Fprintf(opts.Out, "  fetching %s (%s / %s)\n",
							CleanLine(tr.Name), formatRuntimeBytes(tr.Bytes), formatRuntimeBytes(tr.Size))
					}
				}
			}
		}
	}()

	start := time.Now()
	installRes, err := conn.RuntimeFetch(ctx, api.RuntimeFetchArgs{
		Release:    manifest.Release,
		Runtime:    manifest.Runtime,
		Items:      missing,
		LabelItems: allItems,
	})
	stopPoll()
	<-pollDone
	if err != nil {
		return EnsureRuntimeResult{}, err
	}
	if opts.Out != nil {
		for _, f := range installRes.Installed {
			dur := time.Duration(f.DurationMS) * time.Millisecond
			if dur == 0 && !f.AlreadyPresent {
				dur = time.Since(start)
			}
			writeRuntimeProgressLine(opts.Out, f.Name, f.Size, dur)
		}
	}
	return EnsureRuntimeResult{
		Mode:      api.ImagesFetchHost,
		Runtime:   manifest.Runtime,
		Installed: installRes.Installed,
	}, nil
}
