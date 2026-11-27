package image

import "testing"

func TestDefaultInputsRequireExplicitOpenSSHClientObservation(t *testing.T) {
	inputs, lock := defaultFixtureInputs(t)
	missing := inputs
	missing.Packages = nil
	found := false
	for _, pkg := range inputs.Packages {
		if pkg.Name == "openssh-client" {
			found = true
			continue
		}
		missing.Packages = append(missing.Packages, pkg)
	}
	if !found {
		t.Fatal("the default image relies on a recommended dependency for Git SSH transport")
	}
	if err := inputs.Validate(lock); err != nil {
		t.Fatal(err)
	}
	if err := missing.Validate(lock); err == nil {
		t.Fatal("a root without an actual OpenSSH package observation became a default image pin")
	}
}
