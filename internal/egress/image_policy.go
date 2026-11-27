package egress

import "strings"

func validateImagePolicyValue(key, value string) (bool, error) {
	switch key {
	case "sandbox.image_policy.identity", "sandbox.image_policy.issuer", "sandbox.image_policy.trusted_root":
		if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return true, badLayerValue(key, "needs a value of at most 4096 bytes without control separators")
		}
		return true, nil
	default:
		return false, nil
	}
}
