// Command host is the helper the client installs on each host. It keeps sessions
// alive and serves the client over the SSH channel's stdio.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

const usage = `%[1]s keeps agent sessions alive on this host. The client runs it over SSH.

Usage:
  %[1]s bridge              serve one client on stdin and stdout
  %[1]s ls [--json]         list sessions
  %[1]s new [--agent a] [--dir d [--mkdir]] [--name n] [--setup|--no-setup] [-- argv…]
  %[1]s rm <id>             stop a session and remove it
  %[1]s doctor [--json]     check this host
  %[1]s keytest             print every key the terminal sends
  %[1]s restore [--all]     start again sessions a restart (or, with --all, a crash) stopped
  %[1]s idle setup <30m|never>   power this VM off when idle (a user timer runs "idle check")
  %[1]s prune               remove the helper versions nothing uses any more
  %[1]s version
`

// commands are the helper's commands. Each gets the arguments after its name.
var commands = map[string]func(args []string) error{
	"bridge": func(args []string) error {
		return host.RunBridge(host.BridgeInput(os.Stdin, args), os.Stdout)
	},
	"idle":    runIdle,
	"holder":  runHolder,
	"ls":      runLs,
	"new":     runNew,
	"rm":      runRm,
	"doctor":  runDoctor,
	"hook":    runHook,
	"restore": runRestore,
	"prune":   runPrune,
	"keytest": func([]string) error { return host.KeyTest() },
	// The phone path: experimental, and left out of the usage on purpose
	// until it is launched (docs/contributing/phone-path.md).
	"pick":      func([]string) error { return host.RunPick(os.Stdin, os.Stdout) },
	"version":   printVersion,
	"--version": printVersion,
	"help":      printUsage,
	"-h":        printUsage,
	"--help":    printUsage,
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, brand.HostBinary)
		os.Exit(2)
	}
	run, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: unknown command %q\n", os.Args[1])
		os.Exit(1)
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		var pe *proto.Error
		if errors.As(err, &pe) && pe.Hint != "" {
			fmt.Fprintf(os.Stderr, "\nTry: %s\n", pe.Hint)
		}
		os.Exit(1)
	}
}

func runIdle(args []string) error {
	msg, err := host.RunIdle(args)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func runHolder(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: %s holder <id>", brand.HostBinary)
	}
	return host.RunHolder(args[0])
}

func runLs(args []string) error {
	list, err := host.List()
	if err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		return printJSON(list)
	}
	for _, s := range list {
		fmt.Printf("%-10s %-20s %-9s %-12s %s\n", s.ID, s.Name, s.Agent, s.State, s.Cwd)
	}
	return nil
}

func runNew(args []string) error {
	a := host.NewArgs{Term: os.Getenv("TERM"), ColorTerm: os.Getenv("COLORTERM"),
		TermProgram: os.Getenv("TERM_PROGRAM"), TermProgramVersion: os.Getenv("TERM_PROGRAM_VERSION")}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			i++
			a.Agent = arg(args, i)
		case "--dir":
			i++
			a.Cwd = arg(args, i)
		case "--mkdir":
			a.MakeFolder = true
		case "--name":
			i++
			a.Name = arg(args, i)
		case "--setup":
			a.Setup = api.SetupRun
		case "--no-setup":
			a.Setup = api.SetupSkip
		case "--":
			a.Argv = args[i+1:]
			i = len(args)
		}
	}
	info, err := host.New(a)
	if err != nil {
		return err
	}
	return printJSON(info)
}

func runRm(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: %s rm <id>", brand.HostBinary)
	}
	res, err := host.Delete(args[0])
	if err != nil {
		return err
	}
	if res.Worktree != "" {
		fmt.Println(res.Worktree)
	}
	return nil
}

func runDoctor(args []string) error {
	d := host.Doctor()
	if hasFlag(args, "--json") {
		return printJSON(d)
	}
	fmt.Printf("%s %s on %s (%s, %s/%s)\n", brand.HostBinary, d.Version, d.Hostname, d.Distro, d.OS, d.Arch)
	for _, c := range d.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✕"
		}
		fmt.Printf("  %s %-22s %s\n", mark, c.Name, c.Detail)
		if !c.OK && c.Fix != "" {
			fmt.Printf("    Try: %s\n", c.Fix)
		}
	}
	found := make([]string, 0, len(d.Agents))
	for k := range d.Agents {
		found = append(found, k)
	}
	sort.Strings(found)
	fmt.Printf("  agents: %s\n", strings.Join(found, ", "))
	return nil
}

func runHook(args []string) error {
	if len(args) >= 1 {
		host.RunHook(args[0], args[1:])
	}
	return nil
}

func runRestore(args []string) error {
	res := host.Restore(hasFlag(args, "--all"))
	if hasFlag(args, "--json") {
		return printJSON(res)
	}
	for _, r := range res {
		fmt.Printf("%-10s %-20s %-9s %s\n", r.ID, r.Name, r.Action, r.Why)
	}
	return nil
}

func runPrune([]string) error {
	removed, err := host.Prune()
	if err != nil {
		return err
	}
	fmt.Printf("removed %d old helper versions\n", len(removed))
	return nil
}

func printVersion([]string) error {
	fmt.Printf("%s %s (protocol %s)\n", brand.HostBinary, version.Version, version.Protocol)
	return nil
}

func printUsage([]string) error {
	fmt.Printf(usage, brand.HostBinary)
	return nil
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func hasFlag(args []string, f string) bool {
	return slices.Contains(args, f)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
