package main

func sshKeyConfigFields(repo string) ([]field, []field, error) {
	keys, err := commentedConfigFields(repo, "internal/api/layer_values.go", "SandboxValues", "sandbox", "ssh_keys")
	if err != nil {
		return nil, nil, err
	}
	kind, err := commentedConfigFields(repo, "internal/client/secret_definition.go", "SecretDefinition", "secrets.<name>", "kind")
	if err != nil {
		return nil, nil, err
	}
	keys[0].def = "`[]`"
	kind[0].def = "`\"\"`"
	return keys, kind, nil
}
