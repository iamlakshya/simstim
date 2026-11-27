package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func newCmd() *cobra.Command {
	var agent, dir, name, base, memoryMax, wallLevel, profile, preset, task, inside, imageRef string
	var detach, worktree, inline, setup, noSetup, mkdir, asJSON, downgrade, untrusted, sshAgent bool
	var secrets, expose, mounts, include, sets, allow, deny, seeds, copies []string
	var seedDirty, dryRun, allowWake bool
	var needs []string
	var machineProvider string
	var environment []client.EnvironmentOption
	var count int
	var batch []map[string]any
	cmd := &cobra.Command{
		Use:   "new [<host>|-] [-- <command>…]",
		Short: "Start a session on a host, and attach to it",
		Long: "Starts one session and attaches. --detach leaves it running. --count starts independent sessions\n" +
			"without attaching. Without a terminal, a command after -- runs through sandbox exec and returns\n" +
			"its exit code. Its completed sandbox stops and keeps its work for pull.",
		Example: "  " + brand.Name + " new my-vps --agent claude -C ~/src/app\n" +
			"  " + brand.Name + " new my-vps --agent claude --dir ~/src/new-app --mkdir\n" +
			"  " + brand.Name + " new my-vps --agent claude --secret ANTHROPIC_API_KEY\n" +
			"  " + brand.Name + " new my-vps --agent claude --memory-max 6G\n" +
			"  " + brand.Name + " new my-vps --detach -- htop\n" +
			"  " + brand.Name + " new my-vps --agent claude --detach --json",
		Args:              optionalNewHost,
		ValidArgsFunction: completeNewHost,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			if err := checkNewMachineFlagsV1(cmd, args, machineProvider); err != nil {
				return err
			}
			if _, err := client.ReadPolicySettings(); err != nil {
				return err
			}
			if strings.ContainsAny(task, "\r\n") {
				return policyUsage("--task must be one line", "Describe the work on one line.", "bad_task")
			}
			if cmd.Flags().Changed("in") {
				if len(needs) != 0 || dryRun || allowWake {
					return policyUsage("automatic placement cannot select an existing sandbox member", "Use new auto for a fresh sandbox, or omit --need, --dry-run and --allow-wake.", "flags_conflict")
				}
				if err := client.CheckSessionName(name); err != nil {
					return err
				}
				return runFreshMemberNew(cmd, args, inside, agent, name, task, detach, asJSON)
			}
			execArgv := nonTerminalNewCommand(cmd, args, detach)
			if err := beforeAttach(cmd, asJSON, detach || dryRun || len(execArgv) > 0 && !asJSON); err != nil {
				return err
			}
			if err := client.CheckSessionName(name); err != nil {
				return err
			}
			requestedWall := ""
			if cmd.Flags().Changed("wall") {
				requestedWall = wallLevel
			}
			if err := client.ValidateWallRequest(requestedWall, downgrade); err != nil {
				return err
			}
			if setup && noSetup {
				return &client.UserError{Msg: "--setup-script and --no-setup-script cannot go together", Hint: "Pass one of them.", Code: "flags_conflict", Kind: client.KindUsage}
			}
			var memMax int64
			var err error
			if cmd.Flags().Changed("memory-max") {
				if memMax, err = client.ParseMemoryFlag(memoryMax); err != nil {
					return err
				}
			}
			a := client.NewArgs{Agent: agent, Cwd: client.RemotePath(dir), MakeFolder: mkdir, Name: name, Worktree: worktree, Base: base, Inline: inline, MemoryMax: memMax, SSHAgent: sshAgent, Downgrade: downgrade, Mounts: mounts, Include: include}
			a.Wall, a.Task = requestedWall, task
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				a.Argv = args[dash:]
			}
			if len(execArgv) > 0 {
				a.Argv = nil
			}
			if setup || noSetup {
				a.Setup = setupValue(setup)
			}
			values, err := client.ParseLayerSets(sets)
			if err != nil {
				return err
			}
			values.Sandbox.Mounts = append(values.Sandbox.Mounts, mounts...)
			values.Sandbox.Include = append(values.Sandbox.Include, include...)
			values.Sandbox.CopyIn = append(values.Sandbox.CopyIn, copies...)
			values.Sandbox.Secrets = append(values.Sandbox.Secrets, secrets...)
			values.Sandbox.Secrets = append(values.Sandbox.Secrets, expose...)
			if requestedWall != "" {
				values.Sandbox.Wall = &requestedWall
			}
			if cmd.Flags().Changed("image") {
				values.Sandbox.Image = &imageRef
			}
			if cmd.Flags().Changed("preset") {
				values.Sandbox.Preset = &preset
			}
			values.Network.Allow = append(values.Network.Allow, allow...)
			values.Network.Deny = append(values.Network.Deny, deny...)
			options := client.NewInputOptions{Seeds: seeds, SeedDirty: seedDirty, HostClone: cmd.Flags().Changed("dir") || cmd.Flags().Changed("d"), Profile: profile, Session: values, Environment: environment}
			if !options.HostClone {
				a.Cwd = ""
			}
			originalContext := cmd.Context()
			selectedHost := selectedNewArgumentV1(cmd, args)
			var machineOwner *client.SessionMachineOwnerV1
			if machineProvider != "" {
				machineOwner, a, err = prepareNewMachineV1(cmd, machineProvider, a)
				if machineOwner != nil {
					defer func() { runErr = errors.Join(runErr, machineOwner.Finish(originalContext)) }()
				}
				if err != nil {
					return err
				}
				selectedHost = machineOwner.Host().Name
				callerContext := originalContext
				defer cmd.SetContext(callerContext)
				originalContext = machineOwner.Context(originalContext)
			}
			selection, err := client.PrepareHostSelection(originalContext, selectedHost, options)
			if err != nil {
				return err
			}
			need, err := client.ParsePlacementNeed(needs)
			if err != nil {
				return errors.Join(err, selection.Close())
			}
			need.Untrusted = untrusted
			if !selection.Automatic() && (len(needs) != 0 || allowWake) {
				return errors.Join(policyUsage("automatic placement options need auto", "Use new auto with --need or --allow-wake.", "flags_conflict"), selection.Close())
			}
			wake := client.AutoPlacementOptionsV1{AllowWake: allowWake, Progress: prompts(cmd).out}
			if dryRun {
				defer func() { runErr = errors.Join(runErr, selection.Close()) }()
				return dryRunNewPlacementV1(cmd, selection, a, need, wake)
			}
			plan, err := client.NewHostAttemptPlanV1(originalContext, selection, a, need, wake)
			if plan != nil {
				defer func() { runErr = errors.Join(runErr, plan.Close()) }()
			}
			if err != nil {
				if plan == nil {
					err = errors.Join(err, selection.Close())
				}
				return err
			}
			defer cmd.SetContext(originalContext)
			var chosen *client.HostAttemptV1
			var h client.Host
			var conn *client.Conn
			var session *client.Session
			for {
				cmd.SetContext(originalContext)
				attempt, nextErr, answered := nextSelectedNewAttemptV1(cmd, plan)
				if nextErr != nil {
					if attempt == nil || !answered {
						return nextErr
					}
					retry, settleErr := plan.Retry(originalContext, attempt, nextErr)
					if settleErr != nil {
						return errors.Join(nextErr, settleErr)
					}
					if retry {
						continue
					}
					return nextErr
				}
				chosen, h, conn = attempt, attempt.Host(), attempt.Conn()
				cmd.SetContext(attempt.Context())
				if len(execArgv) > 0 && !conn.HasFeature(api.FeatureExecStream) {
					return &client.UserError{Msg: "the helper does not offer sandbox exec", Hint: "Upgrade the host helper, then run the command again.", Code: "upgrade_required"}
				}
				prepared := attempt.Args()
				cols, rows := termSize()
				if err := newStartStep(cmd, func(ctx context.Context) error {
					prepared, err = client.PrepareNewSessionArgs(ctx, prepared, cols, rows)
					return err
				}); err != nil {
					return err
				}
				prepared, err = prepareWallStart(cmd, conn, h, prepared, attempt.Request(), options, secrets, expose)
				if err != nil {
					return err
				}
				var actualResult bool
				session, err, actualResult = startSessionOnAttemptV1(cmd, attempt, prepared)
				if err != nil {
					if !actualResult {
						return err
					}
					retry, settleErr := plan.Retry(originalContext, attempt, err)
					if settleErr != nil {
						return errors.Join(err, settleErr)
					}
					if retry {
						continue
					}
					return err
				}
				if err := checkSessionID(h.Name, session.ID); err != nil {
					return err
				}
				if err := attempt.Request().Close(); err != nil {
					return err
				}
				cmd.SetContext(originalContext)
				break
			}
			s := session
			closeBridge := chosen.Close
			if machineOwner != nil {
				if err := closeBridge(); err != nil {
					return err
				}
				if err := machineOwner.Commit(originalContext); err != nil {
					return err
				}
				h = machineOwner.Host()
				if len(execArgv) > 0 {
					conn, err = client.Dial(originalContext, h, false)
					if err != nil {
						return err
					}
					defer conn.Close()
				}
			}
			// An address without a host finds the new session at once.
			client.RememberSession(h.Name, *s)
			wallStartNotes(cmd, *s)
			if len(execArgv) > 0 {
				return runNewCommand(cmd, conn, *s, execArgv)
			}
			if !detach {
				if err := closeBridge(); err != nil {
					return err
				}
				return attach(cmd.Context(), h, s.ID, s.Name)
			}
			if err := closeBridge(); err != nil {
				return err
			}
			if cmd.Flags().Changed("count") {
				batch = append(batch, sessionObject(h.Name, *s))
				if !asJSON {
					say(cmd, "Started %s/%s (%s, %s).\n", shown(h.Name), shown(s.ID), shown(s.Name), shown(s.Agent))
				}
				return nil
			}
			if asJSON {
				return printJSON(sessionObject(h.Name, *s))
			}
			say(cmd, "Started %s/%s (%s, %s).\n", shown(h.Name), shown(s.ID), shown(s.Name), shown(s.Agent))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&machineProvider, "machine", "", "make one provider machine for this session (e2b), removed by rm")
	f.StringArrayVar(&needs, "need", nil, "require automatic placement architecture, tier or local features (repeatable)")
	addDryRun(cmd, &dryRun, "show automatic placement without starting or waking a host")
	f.BoolVar(&allowWake, "allow-wake", false, "allow an automatic paid wake without its extra question")
	f.StringVarP(&agent, "agent", "a", "shell", "the agent: "+agentKinds())
	f.StringVarP(&dir, "dir", "C", "~", "the folder on the host")
	f.BoolVar(&mkdir, "mkdir", false, "make the folder on the host if it does not exist, without asking")
	addName(cmd, &name, "a name for the session (default: the folder name)")
	addIn(cmd, &inside, "start a fresh agent in this host's existing sandbox session")
	addDetach(cmd, &detach, "start it without attaching")
	addCount(cmd, &count, "start this many sessions without attaching, named <name>-1 to <name>-<count>")
	f.BoolVarP(&worktree, "worktree", "w", false, "give the session its own git worktree and branch")
	f.BoolVar(&inline, "inline", false, "start the agent in its inline mode, so your terminal keeps its scrollback (see agents.inline in config.toml)")
	f.StringVar(&base, "base", "", "the commit or branch the worktree starts from (default: HEAD)")
	addMemoryMax(cmd, &memoryMax, "cap the session's memory: auto, or a `size` such as 4G (default: sessions.memory_max in config.toml)")
	addSecret(cmd, &secrets, "pass secrets from config.toml's [secrets] into the session's environment, such as A,B, or the flag again")
	f.StringVar(&wallLevel, "wall", "auto", "the wall: auto (the host floor), none, process, gvisor or microvm")
	f.StringVar(&imageRef, "image", "", "the pinned image or an image saved on this host")
	f.BoolVar(&downgrade, "downgrade", false, "run this start below the floor, with an explicit --wall level")
	f.StringArrayVar(&expose, "expose", nil, "explicitly expose a named secret in the session (repeatable)")
	f.StringArrayVar(&mounts, "mount", nil, "grant a host folder to the sandbox, read-only by default (repeatable)")
	f.StringArrayVar(&include, "include", nil, "copy a regular source file into the sandbox's clone (repeatable)")
	f.StringArrayVar(&seeds, "seed", nil, "seed a laptop Git repository into /work/<basename> (repeatable)")
	f.BoolVar(&seedDirty, "seed-dirty", false, "include the original dirty seed snapshot in one prospective commit")
	f.StringArrayVar(&copies, "copy", nil, "copy one laptop file as source:guest-path (repeatable)")
	addEnvironmentFlags(f, &environment)
	f.BoolVar(&untrusted, "untrusted", false, "cap this session's grants at the throwaway tier")
	f.BoolVar(&sshAgent, "ssh-agent", false, "grant the laptop's SSH agent to one unwalled session on an own host")
	f.BoolVar(&setup, "setup-script", false, "run the repository's setup script this time, whatever your answer for it")
	f.BoolVar(&noSetup, "no-setup-script", false, "do not run the repository's setup script this time")
	addOldNewFlags(f, &dir, &setup, &noSetup)
	addProfile(cmd, &profile, "a named policy profile from config.toml")
	_ = cmd.RegisterFlagCompletionFunc("profile", completeProfiles)
	cmd.Flags().StringArrayVar(&sets, "set", nil, "set one policy key=value for this session (repeatable)")
	cmd.Flags().StringVar(&preset, "preset", "balanced", "the network preset: locked, balanced or open")
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "allow a network destination for this session (repeatable)")
	cmd.Flags().StringArrayVar(&deny, "deny", nil, "deny a network destination for this session (repeatable)")
	cmd.Flags().StringVar(&task, "task", "", "one line about this session's work")
	addJSON(cmd, &asJSON)
	runOne := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runCountedNew(cmd, args, count, &name, &detach, asJSON, &batch, runOne)
	}
	return cmd
}
