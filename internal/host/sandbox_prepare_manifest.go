package host

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func buildSandboxPrepareManifestV1(ctx context.Context, id, trueBin string, witness *sandboxPrepareWitnessV1) (*Manifest, error) {
	if ctx == nil {
		return nil, newRuntimeError("fixed preparation has no request lifetime")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if witness == nil || !validID(id) {
		return nil, sessionLeaseError("sandbox prepare manifest has no valid reservation or witness")
	}
	microVM := witness.level == "microvm"
	argv := []string{"/bin/true"}
	var pathDirs []string
	if microVM {
		pathDirs = []string{"/opt/agents/bin"}
	} else {
		if trueBin == "" || !filepath.IsAbs(trueBin) || filepath.Base(trueBin) != "true" {
			return nil, sandboxBootError("cannot resolve the host true executable for sandbox preparation", "Ensure /usr/bin/true or /bin/true is installed on the host.")
		}
		argv = []string{trueBin}
	}
	cpus := api.SandboxPrepareCPUsV1
	diskMax := sandboxPrepareDiskStringV1
	keepCaches := false
	decoys := false
	policy, layers, err := egress.MergeLayers([]api.Layer{{
		Kind:    api.LayerSession,
		Preset:  api.PresetLocked,
		Unknown: api.UnknownDeny,
		LayerValues: api.LayerValues{
			Sandbox: api.SandboxValues{
				CPUs:       &cpus,
				DiskMax:    &diskMax,
				KeepCaches: &keepCaches,
				Decoys:     &decoys,
			},
		},
	}})
	if err != nil {
		return nil, err
	}
	var limits Limits
	reason := "shares the host kernel"
	if microVM {
		budget, err := vmneed.BudgetFor(api.SandboxPrepareGuestRAMV1)
		if err != nil {
			return nil, err
		}
		limits = Limits{Declared: budget.Limit, MemoryMax: budget.Limit, MemoryAsked: api.SandboxPrepareGuestRAMV1}
		reason = "runs in its own guest kernel"
	} else {
		limits = Limits{Declared: api.SandboxPrepareGuestRAMV1, MemoryMax: api.SandboxPrepareGuestRAMV1, MemoryAsked: api.SandboxPrepareGuestRAMV1}
	}
	env, term := walledEnv(NewArgs{Term: "xterm-256color"}, id, pathDirs, agents.Agent{}, false)
	now := time.Now().UTC()
	m := &Manifest{
		Schema:      1,
		ID:          id,
		Name:        "sandbox-prepare-" + id,
		CreatedAt:   now,
		HostVersion: version.Version,
		Agent:       AgentInfo{Kind: "custom", Argv: argv, Command: slices.Clone(argv)},
		Cwd:         "/work/scratch",
		Env:         env,
		Term:        TermInfo{Term: term, Cols: 80, Rows: 24},
		Limits:      limits,
		Run:         Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:      Status{State: StateStarting, Since: now},
		Restart:     Restart{Policy: "never"},
		Labels:      map[string]string{sandboxPrepareLabelKeyV1: "1"},
		Wall:        &WallRecord{Level: witness.level, Driver: witness.driver, Version: version.Version, Reason: reason, At: now},
		Policy:      &policy,
		Layers:      layers,
		Files:       &FilePlan{Seed: Seed{Repo: "scratch", Scratch: true}},
	}
	if witness.runtime != nil {
		selected, err := selectedRuntimeInventoryV1(ctx, witness.runtime, witness.runtimeUse)
		if err != nil {
			return nil, err
		}
		m.RuntimeManifest = &selected
	}
	if microVM {
		if witness.need == nil || len(witness.needBytes) == 0 {
			return nil, newRuntimeError("the microvm sandbox preparation has no verified VM Need")
		}
		if err := witness.need.ValidateWall(runtime.GOARCH, m.Wall.Level, m.Wall.Driver, m.Limits.MemoryMax); err != nil {
			return nil, err
		}
		m.VMNeedV1 = bytes.Clone(witness.needBytes)
		m.Agents = witness.need.Agents.SHA256
		m.Run.Image = witness.need.Image.PlatformDigest
	}
	if err := ValidateManifestStart(m); err != nil {
		return nil, err
	}
	if err := validateSandboxPrepareBootManifestV1(m); err != nil {
		return nil, err
	}
	return m, nil
}

func validateSandboxPrepareBootManifestV1(m *Manifest) error {
	if !IsSandboxPrepareManifestV1(m) || m.Wall == nil || m.SandboxOf != "" || m.Restart.Policy != "never" ||
		len(m.Secrets) != 0 || m.SecretState != nil || m.Inputs != nil || m.SSHAgent || m.Git != nil ||
		m.SetupHook != "" || m.ResumeHook != "" || m.RunHook != "" || len(m.AutomaticRestore) != 0 {
		return sessionLeaseError("the sandbox-prepare manifest violates its fixed isolation invariants")
	}
	var pathDirs []string
	if m.Wall.Level == "microvm" {
		pathDirs = []string{"/opt/agents/bin"}
	}
	env, term := walledEnv(NewArgs{Term: "xterm-256color"}, m.ID, pathDirs, agents.Agent{}, false)
	if m.Cwd != "/work/scratch" || m.Term != (TermInfo{Term: term, Cols: 80, Rows: 24}) || !slices.Equal(m.Env, env) ||
		m.Agent.Kind != "custom" || !slices.Equal(m.Agent.Command, m.Agent.Argv) || len(m.Agent.ResumeArgv) != 0 ||
		len(m.Agent.ContinueArgv) != 0 || m.Agent.AgentSessionID != "" || m.Agent.IDSource != "" || len(m.Agent.IDHistory) != 0 ||
		len(m.Agent.Hooks) != 0 || len(m.Agent.Inline) != 0 || len(m.Labels) != 1 || m.Limits.MemoryAsked != api.SandboxPrepareGuestRAMV1 ||
		m.ResumePreparation != nil || m.Auxiliary != nil || len(m.PolicyNext) != 0 || m.VMCaptureNonce != "" ||
		len(m.MissingSecrets) != 0 || len(m.SeedHeads) != 0 || len(m.Pulled) != 0 || m.CopiesPending || len(m.extra) != 0 {
		return sessionLeaseError("the sandbox-prepare command, environment or closed input plan changed")
	}
	if len(m.Agent.Argv) != 1 || filepath.Base(m.Agent.Argv[0]) != "true" || !filepath.IsAbs(m.Agent.Argv[0]) {
		return sessionLeaseError("the sandbox-prepare manifest does not execute the fixed true command")
	}
	if !reflect.DeepEqual(m.Files, &FilePlan{Seed: Seed{Repo: "scratch", Scratch: true}}) {
		return sessionLeaseError("the sandbox-prepare manifest carries unexpected host files or seed data")
	}
	if m.Policy == nil || m.Policy.Preset != api.PresetLocked || m.Policy.Unknown != api.UnknownDeny || len(m.Policy.Rules) != 0 ||
		m.Policy.Sandbox.CPUs == nil || *m.Policy.Sandbox.CPUs != api.SandboxPrepareCPUsV1 ||
		m.Policy.Sandbox.DiskMax == nil || *m.Policy.Sandbox.DiskMax != sandboxPrepareDiskStringV1 {
		return sessionLeaseError("the sandbox-prepare manifest does not carry its fixed locked 1 CPU and 1 GiB policy")
	}
	if m.Policy.Sandbox.KeepCaches == nil || *m.Policy.Sandbox.KeepCaches || m.Policy.Sandbox.Decoys == nil || *m.Policy.Sandbox.Decoys {
		return sessionLeaseError("the sandbox-prepare policy acquired cache retention or decoys")
	}
	switch m.Wall.Level {
	case "process":
		if m.Wall.Driver != "process" || m.RuntimeManifest != nil || len(m.VMNeedV1) != 0 ||
			m.Limits.MemoryMax != api.SandboxPrepareGuestRAMV1 || m.Limits.Declared != api.SandboxPrepareGuestRAMV1 {
			return sessionLeaseError("the process sandbox-prepare manifest violates its fixed 512 MiB cap")
		}
	case "gvisor":
		if m.Wall.Driver != "gvisor" || m.RuntimeManifest == nil || len(m.VMNeedV1) != 0 ||
			m.Limits.MemoryMax != api.SandboxPrepareGuestRAMV1 || m.Limits.Declared != api.SandboxPrepareGuestRAMV1 {
			return sessionLeaseError("the gvisor sandbox-prepare manifest violates its fixed 512 MiB cap or runtime")
		}
	case "microvm":
		budget, err := vmneed.BudgetFor(api.SandboxPrepareGuestRAMV1)
		if err != nil || m.RuntimeManifest == nil || len(m.VMNeedV1) == 0 ||
			m.Limits.MemoryMax != budget.Limit || m.Limits.Declared != budget.Limit {
			return sessionLeaseError("the microvm sandbox-prepare manifest violates its fixed VM memory budget")
		}
		need, err := vmneed.DecodeV1(m.VMNeedV1, runtime.GOARCH)
		if err != nil || need.GuestRAM != api.SandboxPrepareGuestRAMV1 || need.CPUs != api.SandboxPrepareCPUsV1 ||
			need.DiskMax != api.SandboxPrepareDiskV1 || need.ValidateWall(runtime.GOARCH, m.Wall.Level, m.Wall.Driver, m.Limits.MemoryMax) != nil {
			return sessionLeaseError("the microvm sandbox-prepare manifest carries an invalid VM Need")
		}
	default:
		return sessionLeaseError("the sandbox-prepare manifest names an unsupported wall level")
	}
	return nil
}
