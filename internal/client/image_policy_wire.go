package client

import (
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

// Local source pins retain the original paths. The actual physical writer
// refuses an unprojected typed start even if another caller bypasses the
// ordinary frontend's explicit clone projection.
func checkClientImagePolicyWireV1(operation string, args any) error {
	var layers []api.Layer
	switch operation {
	case "new":
		a, ok := args.(NewArgs)
		if !ok {
			return nil
		}
		layers = a.Layers
	case "resume":
		a, ok := args.(ResumeArgs)
		if !ok {
			return nil
		}
		layers = a.Layers
	case api.OpRewindPrepareV1:
		a, ok := args.(api.RewindPrepareArgsV1)
		if !ok {
			return imageSelectionHistoryError()
		}
		layers = a.Start.Layers
	case api.OpRewindCommitV1:
		a, ok := args.(api.RewindCommitArgsV1)
		if !ok {
			return imageSelectionHistoryError()
		}
		layers = a.Start.Layers
	default:
		return nil
	}
	if !reflect.DeepEqual(layers, api.ProjectClientImagePolicy(layers)) {
		return &UserError{Msg: "the outgoing session policy contains a laptop trusted root path", Hint: "Prepare this operation through the original local policy selection.", Code: "image_policy_source", Kind: KindRefused}
	}
	return nil
}
