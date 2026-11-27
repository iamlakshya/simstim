package main

// imagePolicyConfigFields reads the actual shared image schema comments.
func imagePolicyConfigFields(repo string) ([]field, error) {
	return commentedConfigFields(repo, "internal/api/image_policy.go", "ImagePolicyValues", "sandbox.image_policy", "identity", "issuer", "trusted_root")
}
