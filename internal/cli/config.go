package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/tui"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the configuration",
		Example: "  " + brand.Name + " config path\n" +
			"  " + brand.Name + " config keymap",
	}
	var asJSON bool
	keymap := &cobra.Command{
		Use:   "keymap",
		Short: "Print the launcher's commands and their keys",
		Long:  "Prints the launcher's commands and the keys in effect. Rebind a key under [keymap] in config.toml.",
		Example: "  " + brand.Name + " config keymap\n" +
			"  " + brand.Name + " config keymap --json",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			entries := tui.Keymap(client.LoadSettings())
			if asJSON {
				for i := range entries {
					if entries[i].Keys == nil {
						entries[i].Keys = []string{} // a command of the palette only: a list, never null
					}
				}
				return printJSON(entries)
			}
			for _, e := range entries {
				keys := strings.Join(e.Keys, ", ")
				if keys == "" {
					keys = "(palette only)"
				}
				fmt.Printf("%-32s %s\n", e.Command, keys)
			}
			return nil
		},
	}
	addJSON(keymap, &asJSON)
	cmd.AddCommand(keymap, &cobra.Command{
		Use:     "path",
		Short:   "Print where the configuration lives",
		Example: "  " + brand.Name + " config path",
		Args:    cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Println(filepath.Join(client.ConfigDir(), "config.toml"))
			fmt.Println(filepath.Join(client.ConfigDir(), "hosts.toml"))
		},
	})
	return cmd
}
