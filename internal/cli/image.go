package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/image"
)

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "List, pull and remove session images on a host",
		Example: "  " + brand.Name + " image ls my-vps\n" +
			"  " + brand.Name + " image pull default my-vps",
	}
	cmd.AddCommand(imageLsCmd(), imagePullCmd(), imageRmCmd())
	return cmd
}

func imageLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls [<host>]",
		Short: "List the session images stored on a host",
		Example: "  " + brand.Name + " image ls my-vps\n" +
			"  " + brand.Name + " image ls my-vps --json",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostName := ""
			if len(args) == 1 {
				hostName = args[0]
			}
			h, err := pickHost(hostName)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			conn, err := client.Dial(ctx, h, inputOn(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			list, err := conn.Images(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(list)
			}
			if len(list) == 0 {
				say(cmd, "No images stored on %s.\n", h.Name)
				return nil
			}
			for _, img := range list {
				printf("%-19s %-7s %8s  %s\n", shortDigestCol(img.PlatformDigest), img.Arch, formatImageSize(img.Size), client.CleanLine(img.Ref))
			}
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func imagePullCmd() *cobra.Command {
	var update, asJSON bool
	cmd := &cobra.Command{
		Use:   "pull <ref> <host>",
		Short: "Pin an OCI image and pull it onto a host",
		Long: "Resolves an OCI image reference on this computer, pins its digest in images.lock,\n" +
			"and asks the host to fetch and convert it into its content-addressed store.\n" +
			"Pass `default` to pull the pinned official buildpack-deps:trixie-scm image.",
		Example: "  " + brand.Name + " image pull default my-vps\n" +
			"  " + brand.Name + " image pull ghcr.io/acme/dev:1 my-vps",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, hostName := args[0], args[1]
			h, err := client.FindHost(hostName)
			if err != nil {
				return err
			}
			pin, err := client.ResolveAndPinImage(cmd.Context(), ref, h.Name, update)
			if err != nil {
				return err
			}
			if pin.NewlyPinned {
				say(cmd, "Pinned %s to %s.\n", pin.Ref, shortDigestCol(pin.Digest))
			} else if pin.PreviousDigest != "" {
				say(cmd, "Updated pin for %s: %s -> %s.\n", pin.Ref, shortDigestCol(pin.PreviousDigest), shortDigestCol(pin.Digest))
			}

			conn, err := client.Dial(cmd.Context(), h, inputOn(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()

			info, err := conn.Doctor(cmd.Context())
			if err != nil {
				return err
			}
			expectedRoot := expectedRootHash(pin, info.Arch)

			rec, err := conn.PullImage(cmd.Context(), api.ImagePullArgs{
				Ref:              pin.Ref,
				Digest:           pin.Digest,
				Arch:             info.Arch,
				ExpectedRootHash: expectedRoot,
			})
			if err != nil {
				return err
			}
			if err := client.RecordImageConversion(pin, h.Name, rec); err != nil {
				return err
			}
			if asJSON {
				return printJSON(rec)
			}
			say(cmd, "Stored %s on %s (%s, %s, root %s).\n",
				client.CleanLine(rec.Ref), h.Name, shortDigestCol(rec.PlatformDigest),
				formatImageSize(rec.Size), shortDigestCol(rec.RootHash))
			return nil
		},
	}
	cmd.Flags().BoolVar(&update, "update", false, "update images.lock when a tag moved to a new digest")
	addJSON(cmd, &asJSON)
	return cmd
}

func imageRmCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "rm <digest> <host>",
		Short: "Remove a stored session image from a host",
		Example: "  " + brand.Name + " image rm sha256:ab12cd34 my-vps\n" +
			"  " + brand.Name + " image rm sha256:ab12cd34 my-vps --json",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			digest, hostName := args[0], args[1]
			h, err := client.FindHost(hostName)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			conn, err := client.Dial(ctx, h, inputOn(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			rec, err := conn.RemoveImage(ctx, digest)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(rec)
			}
			say(cmd, "Removed %s (%s) from %s.\n", shortDigestCol(rec.PlatformDigest), client.CleanLine(rec.Ref), h.Name)
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func expectedRootHash(pin client.ImagePinResult, arch string) string {
	if pin.IsDefault {
		if dp, err := image.DefaultForArch(arch); err == nil {
			return dp.RootHash
		}
		return ""
	}
	if p, ok := pin.Entry.Platform(arch); ok {
		return p.RootHash
	}
	return ""
}

func shortDigestCol(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

func formatImageSize(n int64) string {
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// rebuildAgentsDisk best-effort updates the host's read-only agents disk after
// an agent install. It uses its own generous deadline because packaging large
// standalone agent installs can take longer than an ordinary metadata check.
func rebuildAgentsDisk(ctx context.Context, conn *client.Conn) {
	if !conn.HasFeature("images") {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, _ = conn.BuildAgentsDisk(rctx)
}
