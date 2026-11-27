package cli

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/spf13/cobra"
)

func imageCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "image",
		Short:   "List, pull, save and remove microvm root images",
		Example: "  " + brand.Name + " image ls my-vps\n  " + brand.Name + " image pull ubuntu:24.04 my-vps",
	}
	command.AddCommand(imageListCmd(), imagePullCmd(), imageSaveCmd(), imageRemoveCmd())
	return command
}

func imageListCmd() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{Use: "ls [<host>]", Short: "List a host's prepared images", Example: "  " + brand.Name + " image ls\n  " + brand.Name + " image ls my-vps", Args: cobra.MaximumNArgs(1), ValidArgsFunction: completeHostArg, RunE: func(cmd *cobra.Command, args []string) error {
		hosts, err := client.LoadHosts()
		if err != nil {
			return err
		}
		if len(args) == 1 {
			host, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			hosts = []client.Host{host}
		}
		type row struct {
			Host     string `json:"host"`
			Ref      string `json:"ref"`
			Digest   string `json:"digest"`
			Arch     string `json:"arch"`
			RootHash string `json:"root_hash"`
			Size     int64  `json:"size"`
			MadeOn   string `json:"made_on,omitempty"`
		}
		rows := []row{}
		for _, host := range hosts {
			if err := withImageHost(cmd.Context(), host.Name, func(ctx context.Context, _ client.Host, c *client.Conn) error {
				records, err := client.ListImages(ctx, c)
				if err != nil {
					return err
				}
				for _, record := range records {
					madeOn := ""
					if record.SavedHost != nil {
						if c.HostIDChanged() {
							return &client.UserError{Code: "image_protocol", Kind: client.KindFailed, Msg: "the saved image is not on the accepted host identity", Hint: "Check the host identity, then run image ls again."}
						}
						if err := (image.SaveResult{Image: record, HostID: c.HostID()}).Validate(record.Ref, c.HostID()); err != nil {
							return err
						}
						madeOn = host.Name
					}
					rows = append(rows, row{Host: host.Name, Ref: client.CleanLine(record.Ref), Digest: client.CleanLine(record.PlatformDigest), Arch: client.CleanLine(record.Arch), RootHash: client.CleanLine(record.RootHash), Size: record.Size, MadeOn: madeOn})
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if asJSON {
			return printJSON(rows)
		}
		for _, record := range rows {
			fmt.Printf("%s\t%s\t%s\t%s\t%d bytes", record.Host, record.Ref, record.Digest, record.Arch, record.Size)
			if record.MadeOn != "" {
				fmt.Printf("\tmade on %s", client.CleanLine(record.MadeOn))
			}
			fmt.Println()
		}
		return nil
	}}
	addJSON(command, &asJSON)
	return command
}

func imagePullCmd() *cobra.Command {
	var update, asJSON bool
	var mode string
	command := &cobra.Command{Use: "pull <ref> <host>", Short: "Pin an OCI image on this laptop and prepare it on a host", Example: "  " + brand.Name + " image pull ubuntu:24.04 my-vps", Args: cobra.ExactArgs(2), ValidArgsFunction: completeSecondHostArg, RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.ValidateSettings(client.LoadSettings()); err != nil {
			return err
		}
		if mode != "" {
			if _, err := client.RuntimeFetchMode(mode); err != nil {
				return err
			}
		}
		return withImageHost(cmd.Context(), args[1], func(ctx context.Context, _ client.Host, c *client.Conn) error {
			record, err := client.PullImage(ctx, c, args[0], client.ImageOptions{Mode: mode, Update: update, Progress: stderrFor(asJSON, "image_progress")})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(record)
			}
			fmt.Printf("%s\t%s\t%s\n", client.CleanLine(record.Ref), record.PlatformDigest, record.RootHash)
			return nil
		})
	}}
	command.Flags().BoolVar(&update, "update", false, "pin a moved tag after showing its old and new digests")
	addImages(command, &mode, "get image bytes from auto, host or laptop (default: [images] fetch)")
	addJSON(command, &asJSON)
	return command
}

func imageRemoveCmd() *cobra.Command {
	return &cobra.Command{Use: "rm <digest> <host>", Short: "Remove an image that no session uses", Example: "  " + brand.Name + " image rm sha256:abcd my-vps", Args: cobra.ExactArgs(2), ValidArgsFunction: completeSecondHostArg, RunE: func(cmd *cobra.Command, args []string) error {
		return withImageHost(cmd.Context(), args[1], func(ctx context.Context, _ client.Host, c *client.Conn) error {
			return client.RemoveImage(ctx, c, args[0])
		})
	}}
}

func completeSecondHostArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 1 {
		return completeHostArg(cmd, nil, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// Image conversion can outlast the general host operations' 90 second limit.
func withImageHost(ctx context.Context, name string, run func(context.Context, client.Host, *client.Conn) error) error {
	host, err := client.FindHost(name)
	if err != nil {
		return err
	}
	connection, err := client.Dial(ctx, host, true)
	if err != nil {
		return err
	}
	defer connection.Close()
	return run(ctx, host, connection)
}
