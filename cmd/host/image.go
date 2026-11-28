package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
)

func runImage(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s image <ls|pull|rm|agents> [args…]", brand.HostBinary)
	}
	switch args[0] {
	case "ls":
		return runImageLs(args[1:])
	case "pull":
		return runImagePull(args[1:])
	case "rm":
		return runImageRm(args[1:])
	case "agents":
		return runImageAgents(args[1:])
	default:
		return fmt.Errorf("unknown image subcommand %q (expected ls, pull, rm or agents)", args[0])
	}
}

func runImageLs(args []string) error {
	list, err := host.ListImages()
	if err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		return printJSON(list)
	}
	for _, img := range list {
		fmt.Printf("%-19s %-7s %10d %s\n", shortImageDigest(img.PlatformDigest), img.Arch, img.Size, img.Ref)
	}
	return nil
}

func runImagePull(args []string) error {
	var a api.ImagePullArgs
	var asJSON bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--digest":
			i++
			a.Digest = arg(args, i)
		case "--arch":
			i++
			a.Arch = arg(args, i)
		case "--expected-root-hash":
			i++
			a.ExpectedRootHash = arg(args, i)
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %q", args[i])
			}
			if a.Ref == "" {
				a.Ref = args[i]
			}
		}
	}
	if a.Ref == "" {
		return fmt.Errorf("usage: %s image pull <ref> [--digest sha256:…] [--arch amd64|arm64] [--json]", brand.HostBinary)
	}
	rec, err := host.PullImage(context.Background(), a)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(rec)
	}
	fmt.Printf("%s %s root=%s blocks=%d size=%d\n", rec.PlatformDigest, rec.Arch, rec.RootHash, rec.DataBlocks, rec.Size)
	return nil
}

func runImageRm(args []string) error {
	var digest string
	var asJSON bool
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else if digest == "" {
			digest = a
		}
	}
	if digest == "" {
		return fmt.Errorf("usage: %s image rm <digest> [--json]", brand.HostBinary)
	}
	rec, err := host.RemoveImage(digest)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(rec)
	}
	fmt.Printf("removed %s (%s)\n", rec.PlatformDigest, rec.Ref)
	return nil
}

func runImageAgents(args []string) error {
	var (
		rec api.AgentsDiskRecord
		err error
	)
	if hasFlag(args, "--ensure") {
		rec, _, err = host.EnsureAgentsDisk(context.Background())
	} else {
		rec, err = host.BuildAgentsDisk(context.Background())
	}
	if err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		return printJSON(rec)
	}
	fmt.Printf("%s %d bytes (%s)\n", rec.Digest, rec.Size, strings.Join(rec.Agents, ", "))
	return nil
}

func shortImageDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}
