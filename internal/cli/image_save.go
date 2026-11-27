package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/image"
)

func imageSaveCmd() *cobra.Command {
	var allowFlagged, replace, asJSON bool
	cmd := &cobra.Command{
		Use:   "save <host>/<session> <ref>",
		Short: "Save a running microvm's system changes as an image on its host",
		Long: "Saves a running microvm's root image and system changes after a review.\n" +
			"Workspaces, credentials, agent state and temporary files are left out.\n" +
			"Treat the image as private to its host: a login copied to another path can remain in it.",
		Example: "  " + brand.Name + " image save box/work devtools\n  " + brand.Name + " new box --image devtools",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (returned error) {
			if !image.ValidSaveRef(args[1]) {
				return &client.UserError{Code: "bad_image_ref", Kind: client.KindUsage,
					Msg:  "a saved image name needs 1 to 64 lower-case letters, numbers, dots, underscores or hyphens, starting with a letter or number",
					Hint: "Choose a name such as devtools."}
			}
			address, err := sessionArg(cmd, args[:1], false)
			if err != nil {
				return err
			}
			original := cmd.Context()
			request := client.NewImageSaveGrantRequest()
			defer func() {
				returned = errors.Join(returned, request.Close())
				cmd.SetContext(original)
			}()
			ctx := client.WithGrantRequest(client.WithoutWake(original), request)
			cmd.SetContext(ctx)
			return address.open(ctx, cmd, func(_ context.Context, found found) (returned error) {
				options := client.ImageSaveOptions{Replace: replace, AllowFlagged: allowFlagged, Terminal: secretTerminal(cmd)}
				var prepared *client.ImageSave
				handedOff := false
				defer func() {
					if prepared != nil && !handedOff {
						returned = errors.Join(returned, prepared.Close())
					}
				}()
				for {
					operation, err := client.PrepareImageSave(cmd.Context(), found.conn, found.s.ID, args[1], options)
					if operation != nil {
						prepared = operation
					}
					if err == nil {
						handedOff = true
						return finishImageSave(cmd, operation, prompts(cmd), allowFlagged, asJSON)
					}
					var question *client.ConfirmError
					if !errors.As(err, &question) {
						return err
					}
					if err := answerGrant(cmd, err, question); err != nil {
						return err
					}
				}
			})
		},
	}
	cmd.Flags().BoolVar(&allowFlagged, "allow-flagged", false, "save the listed changes that can run code at login or at start after reviewing them")
	cmd.Flags().BoolVar(&replace, "replace", false, "replace this host's existing saved image name")
	addJSON(cmd, &asJSON)
	return cmd
}
