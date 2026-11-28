package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type newP114Opts struct {
	cpus      int
	memory    string
	memoryMax string
	envFlags  []string
	envFiles  []string
	copyFlags []string
	seedDirs  []string
	seedDirty bool
}

type preparedNewP114 struct {
	cpus          int
	memMax        int64
	diskMax       int64
	idleStop      string
	copyMax       int64
	maxRun        time.Duration
	env           map[string]string
	copyItems     []client.CopyInItem
	cliCopySpecs  []string
	seeds         *client.PreparedSeeds
	seedWorkspace bool
	defaultName   string
}

func (p *preparedNewP114) cleanup() {
	if p != nil && p.seeds != nil {
		p.seeds.Cleanup()
	}
}

func (p *preparedNewP114) needsStaging() bool {
	return len(p.copyItems) > 0 || (p.seeds != nil && len(p.seeds.Items) > 0)
}

func parseMemoryPairFlags(cmd *cobra.Command, memory, memoryMax string) (int64, bool, error) {
	hasMem := cmd.Flags().Changed("memory")
	hasMax := cmd.Flags().Changed("memory-max")
	if hasMem && hasMax {
		return 0, false, &client.UserError{
			Msg:  "--memory and --memory-max cannot go together",
			Hint: "Pass one of them, for example --memory 4G.",
			Code: "flags_conflict",
			Kind: client.KindUsage,
		}
	}
	if hasMem {
		n, err := client.ParseMemoryFlagNamed("--memory", memory)
		return n, true, err
	}
	if hasMax {
		n, err := client.ParseMemoryFlag(memoryMax)
		return n, true, err
	}
	return 0, false, nil
}

func parseCPUsFlag(cmd *cobra.Command, cpus int) (int, bool, error) {
	if !cmd.Flags().Changed("cpus") {
		return 0, false, nil
	}
	if cpus <= 0 {
		return 0, false, &client.UserError{
			Msg:  fmt.Sprintf("--cpus: %d is not a positive whole number", cpus),
			Hint: "Pass a positive number of CPUs, such as --cpus 2.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	return cpus, true, nil
}

func prepareNewP114(cmd *cobra.Command, h client.Host, settings client.Settings, dir string, worktree bool, opts newP114Opts) (*preparedNewP114, error) {
	if opts.seedDirty && len(opts.seedDirs) == 0 {
		return nil, &client.UserError{
			Msg:  "--seed-dirty needs --seed",
			Hint: brand.Name + " new " + h.Name + " --seed . --seed-dirty",
			Code: "bad_flag",
			Kind: client.KindUsage,
		}
	}
	if len(opts.seedDirs) > 0 && worktree {
		return nil, &client.UserError{
			Msg:  "--seed and --worktree cannot go together",
			Hint: "Use --seed alone to clone from this laptop, or --worktree for a repository already on the host.",
			Code: "flags_conflict",
			Kind: client.KindUsage,
		}
	}
	memFlag, hasMemFlag, err := parseMemoryPairFlags(cmd, opts.memory, opts.memoryMax)
	if err != nil {
		return nil, err
	}
	cpusFlag, hasCPUsFlag, err := parseCPUsFlag(cmd, opts.cpus)
	if err != nil {
		return nil, err
	}

	sizes, err := client.ResolveSandboxSizes(settings, h)
	if err != nil {
		return nil, err
	}
	prep := &preparedNewP114{
		cpus:     sizes.CPUs,
		memMax:   sizes.Memory,
		diskMax:  sizes.DiskMax,
		idleStop: sizes.IdleStop,
		copyMax:  sizes.CopyMax,
		maxRun:   sizes.MaxRun,
	}
	if hasCPUsFlag {
		prep.cpus = cpusFlag
	}
	if hasMemFlag {
		prep.memMax = memFlag
	}

	env, err := client.ResolveEnv(settings, h, opts.envFiles, opts.envFlags, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	prep.env = env

	copyItems, cliCopySpecs, err := prepareCopyInItems(settings, h, opts.copyFlags, sizes.CopyMax)
	if err != nil {
		return nil, err
	}
	prep.copyItems = copyItems
	prep.cliCopySpecs = cliCopySpecs

	if len(opts.seedDirs) > 0 {
		explicitDir := ""
		if cmd.Flags().Changed("dir") {
			explicitDir = dir
		}
		seeds, err := client.PrepareSeeds(cmd.Context(), opts.seedDirs, explicitDir, opts.seedDirty, sizes.SeedMax, os.Stderr)
		if err != nil {
			return nil, err
		}
		prep.seeds = seeds
		if !cmd.Flags().Changed("dir") {
			prep.seedWorkspace = true
			if len(seeds.Items) == 1 {
				prep.defaultName = seeds.Items[0].Name
			}
		}
	}
	return prep, nil
}

func prepareCopyInItems(settings client.Settings, h client.Host, copyFlags []string, copyMax int64) (items []client.CopyInItem, cliSpecs []string, err error) {
	var copySpecs []string
	copySpecs = append(copySpecs, settings.Sandbox.CopyIn...)
	if h.Sandbox != nil {
		copySpecs = append(copySpecs, h.Sandbox.CopyIn...)
	}
	copySpecs = append(copySpecs, copyFlags...)
	if len(copySpecs) == 0 {
		return nil, nil, nil
	}
	items, err = client.ParseCopyInSpecs(copySpecs, "")
	if err != nil {
		return nil, nil, err
	}
	if err := client.ValidateCopyIn(items, copyMax, client.DefaultCopyInMaxEntries); err != nil {
		return nil, nil, err
	}
	if len(copyFlags) > 0 {
		cliItems, err := client.ParseCopyInSpecs(copyFlags, "")
		if err != nil {
			return nil, nil, err
		}
		cliSpecs = make([]string, len(cliItems))
		for i, it := range cliItems {
			cliSpecs[i] = it.LocalPath + ":" + it.GuestPath
		}
	}
	return items, cliSpecs, nil
}

func startNewWithP114(cmd *cobra.Command, conn *client.Conn, h client.Host, a client.NewArgs, prep *preparedNewP114) (*client.Session, error) {
	a.HostName = h.Name
	if a.In != "" || prep == nil {
		return startSession(cmd, conn, h, a)
	}
	a.CPUs = prep.cpus
	a.DiskMax = prep.diskMax
	a.IdleStop = prep.idleStop
	a.Env = prep.env
	if prep.memMax != 0 {
		a.MemoryMax = prep.memMax
	}
	if prep.maxRun > 0 {
		secs := int64(prep.maxRun / time.Second)
		if secs < 1 {
			secs = 1
		}
		a.MaxRunSeconds = secs
	}
	if !prep.needsStaging() {
		return startSession(cmd, conn, h, a)
	}
	if !conn.HasFeature(api.FeatureSFTP) || !conn.HasFeature(api.FeatureSeed) {
		return nil, &client.UserError{
			Msg:  "the helper on " + h.Name + " is too old for --copy and --seed",
			Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
			Code: "helper_old",
		}
	}
	secrets := a.Secrets
	secretBinds := a.SecretBinds
	exposedSecrets := a.ExposedSecrets
	a.Secrets = nil
	a.SecretBinds = nil
	a.ExposedSecrets = nil
	a.DeferStart = true
	a.CopyIn = client.ToAPICopyInSpecs(prep.copyItems)
	if prep.seedWorkspace {
		a.SeedWorkspace = true
		a.Cwd = ""
		if a.Name == "" && prep.defaultName != "" {
			a.Name = prep.defaultName
		}
	}
	s, err := startSession(cmd, conn, h, a)
	if err != nil {
		return nil, err
	}
	cleanupDeferred := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 15*time.Second)
		defer cancel()
		_, _ = conn.Delete(cctx, s.ID)
	}

	uctx, ucancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer ucancel()
	sc, closer, err := client.OpenStageSFTP(uctx, h, s.ID)
	if err != nil {
		cleanupDeferred()
		return nil, err
	}
	copyFiles, err := client.UploadCopyIn(sc, prep.copyItems, prep.copyMax, client.DefaultCopyInMaxEntries)
	if err != nil {
		_ = closer.Close()
		cleanupDeferred()
		return nil, err
	}
	seedSpecs, err := client.UploadSeedBundles(sc, prep.seeds)
	if err != nil {
		_ = closer.Close()
		cleanupDeferred()
		return nil, err
	}
	if err := closer.Close(); err != nil {
		cleanupDeferred()
		return nil, fmt.Errorf("close staging SFTP connection: %w", err)
	}

	sctx, scancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer scancel()
	started, err := conn.StartDeferred(sctx, api.SessionStartArgs{
		ID:             s.ID,
		CopyFiles:      copyFiles,
		Seeds:          seedSpecs,
		Secrets:        secrets,
		SecretBinds:    secretBinds,
		ExposedSecrets: exposedSecrets,
	})
	if err != nil {
		cleanupDeferred()
		return nil, err
	}
	if len(prep.cliCopySpecs) > 0 {
		_ = client.RememberCopyIn(h.Name, started.ID, prep.cliCopySpecs)
	}
	return started, nil
}

// resendPendingCopies re-uploads configured copy_in files and laptop-saved
// per-session --copy specs on attach when a boot-time restore ran without a
// laptop connected (spec 17.3, question 102). It never reads local paths from
// the remote host's response (G10).
func resendPendingCopies(ctx context.Context, conn *client.Conn, h client.Host, s *client.Session) error {
	if s == nil || !s.CopiesPending {
		return nil
	}
	settings := client.LoadSettings()
	sizes, err := client.ResolveSandboxSizes(settings, h)
	if err != nil {
		return err
	}
	var laptopSpecs []string
	laptopSpecs = append(laptopSpecs, settings.Sandbox.CopyIn...)
	if h.Sandbox != nil {
		laptopSpecs = append(laptopSpecs, h.Sandbox.CopyIn...)
	}
	laptopSpecs = append(laptopSpecs, client.SavedCopyIn(h.Name, s.ID)...)
	items, err := client.ParseCopyInSpecs(laptopSpecs, "")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: %s on %s has pending laptop files from another machine; they were not re-sent.\n",
			client.CleanLine(s.Name), client.CleanLine(h.Name))
		return nil
	}
	if err := client.ValidateCopyIn(items, sizes.CopyMax, client.DefaultCopyInMaxEntries); err != nil {
		return err
	}
	sc, closer, err := client.OpenStageSFTP(ctx, h, s.ID)
	if err != nil {
		return err
	}
	copyFiles, err := client.UploadCopyIn(sc, items, sizes.CopyMax, client.DefaultCopyInMaxEntries)
	closeErr := closer.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close staging SFTP connection: %w", closeErr)
	}
	return conn.ApplyCopyIn(ctx, api.CopyApplyArgs{ID: s.ID, CopyFiles: copyFiles})
}
