package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func newCmd() *cobra.Command {
	var agent, dir, name, base, memoryMax, wallLevel, profile, preset, task, inside, imageRef string
	var detach, worktree, inline, setup, noSetup, mkdir, asJSON, downgrade, untrusted, sshAgent bool
	var secrets, expose, mounts, include, sets, allow, deny, seeds, copies []string
	var seedDirty bool
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
			if _, err := client.ReadPolicySettings(); err != nil {
				return err
			}
			if strings.ContainsAny(task, "\r\n") {
				return policyUsage("--task must be one line", "Describe the work on one line.", "bad_task")
			}
			if cmd.Flags().Changed("in") {
				if err := client.CheckSessionName(name); err != nil {
					return err
				}
				return runFreshMemberNew(cmd, args, inside, agent, name, task, detach, asJSON)
			}
			execArgv := nonTerminalNewCommand(cmd, args, detach)
			if err := beforeAttach(cmd, asJSON, detach || len(execArgv) > 0 && !asJSON); err != nil {
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
			h, err := selectedNewHost(cmd, args)
			if err != nil {
				return err
			}
			if setup && noSetup {
				return &client.UserError{Msg: "--setup-script and --no-setup-script cannot go together", Hint: "Pass one of them.", Code: "flags_conflict", Kind: client.KindUsage}
			}
			var memMax int64
			if cmd.Flags().Changed("memory-max") {
				if memMax, err = client.ParseMemoryFlag(memoryMax); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			conn, err := client.Dial(ctx, h, true)
			if err != nil {
				cancel()
				return err
			}
			cancel()
			var closeOnce sync.Once
			closeBridge := func() { closeOnce.Do(conn.Close) }
			defer closeBridge()
			originalContext := cmd.Context()
			request := client.NewGrantRequest(untrusted, sshAgent)
			cleanup := &newStartCleanup{request: request, budget: originalContext}
			cmd.SetContext(context.WithValue(client.WithGrantRequest(originalContext, request), newStartCleanupKey{}, cleanup))
			defer func() { runErr = errors.Join(runErr, cleanup.finish()); cmd.SetContext(originalContext) }()
			if len(execArgv) > 0 && !conn.HasFeature(api.FeatureExecStream) {
				return &client.UserError{Msg: "the helper does not offer sandbox exec", Hint: "Upgrade the host helper, then run the command again.", Code: "upgrade_required"}
			}
			cols, rows := termSize()
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
			if err := newStartStep(cmd, func(ctx context.Context) error { a, err = client.PrepareNewSessionArgs(ctx, a, cols, rows); return err }); err != nil {
				return err
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
			a, err = prepareWallStart(cmd, conn, h, a, request, options, secrets, expose)
			if err != nil {
				return err
			}
			s, err := startSession(cmd, conn, h, a)
			if err == nil {
				err = checkSessionID(h.Name, s.ID)
			}
			if err != nil {
				return err
			}
			if err := cleanup.finish(); err != nil {
				return err
			}
			cmd.SetContext(originalContext)
			// An address without a host finds the new session at once.
			client.RememberSession(h.Name, *s)
			wallStartNotes(cmd, *s)
			if len(execArgv) > 0 {
				return runNewCommand(cmd, conn, *s, execArgv)
			}
			if !detach {
				closeBridge()
				return attach(cmd.Context(), h, s.ID, s.Name)
			}
			closeBridge()
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
	f.StringVar(&wallLevel, "wall", "none", "the wall: auto (the host floor), none, process, gvisor or microvm")
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
