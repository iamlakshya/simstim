package client

import (
	"fmt"
	"strings"
)

// The ordinary dispatcher may describe this question, but only the original
// image intent can authorise its actual fetch through the scoped answer map.
func allowImageFetchV1(h Host, t Tier, o Outbound) error {
	if !t.Valid() || o.Kind != OutboundImageFetchV1 || strings.TrimSpace(o.Name) == "" || len(o.Name) > 4096 || hasControlRune(o.Name) {
		return &UserError{Msg: "an image fetch needs its exact pinned image", Hint: "Start the request again and approve the missing image it selects.", Code: "bad_grant", Kind: KindUsage}
	}
	if o.Confirmed {
		return nil
	}
	return &ConfirmError{Prompt: fmt.Sprintf("%s lacks %s. Get it now?", hostDisplayName(h), o.Name), Kind: ConfirmYesNo, Host: h.Name, Grant: o, Err: &UserError{Msg: fmt.Sprintf("%s lacks the pinned image %s", hostDisplayName(h), o.Name), Hint: "Prepare the image on this host in an interactive terminal.", Code: "image_fetch_required", Kind: KindRefused}}
}
