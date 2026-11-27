package client

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestLegacyHostSourceInitialisationPreservesUnknownDocument(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("# retained legacy text\nfuture_root = 'unchanged'\n[[host]]\nname = 'box'\nssh = 'box.example'\n[host.future]\nopaque = [1, 2, 3]\n")
	if err := os.WriteFile(hostsPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	_, unlock, err := LockHost(t.Context(), "box", "legacy source")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	got, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := bytes.Cut(got, []byte{'\n'})
	if !ok || !bytes.Equal(body, original) {
		t.Fatalf("marker initialisation re-encoded unknown TOML: %q", got)
	}
}

func TestHostPolicyDocumentUsesOriginalSourceAndPreservesUnknownText(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "box.example", Labels: map[string]string{"team": "old"}}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	marker, _, _ := bytes.Cut(before, []byte{'\n'})
	body := "# retained policy comment\nfuture_root = 'keep'\n[[host]]\nname = 'box'\nssh = 'box.example'\n[host.labels]\nteam = 'old'\n[host.future]\nopaque = [ 1, 2 ]\n"
	original := append(append(bytes.Clone(marker), '\n'), []byte(body)...)
	if err := os.WriteFile(hostsPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(original, []byte("team = 'old'"), []byte("team = 'new'"), 1)
	next := cloneObservationHost(h)
	next.Labels["team"] = "new"
	ctx, unlock, err := LockHost(t.Context(), h.Name, "policy")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := writeHostPolicyDocumentContext(ctx, h, []Host{next}, edited); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	newMarker, keptBody, _ := bytes.Cut(got, []byte{'\n'})
	_, intendedBody, _ := bytes.Cut(edited, []byte{'\n'})
	if bytes.Equal(newMarker, marker) || !bytes.Equal(keptBody, intendedBody) {
		t.Fatalf("policy save did not preserve its document/fresh generation: %q", got)
	}
	saved, err := FindHost(h.Name)
	if err != nil || !SameEntry(saved, next) {
		t.Fatalf("known policy shape diverged: %+v, %v", saved, err)
	}
}

func TestHostPolicyDocumentRefusesStaleRawEpochAndUnrelatedChanges(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "other known host", true: "older raw epoch"}[stale], func(t *testing.T) {
			stateFDTestHome(t)
			h := Host{Name: "box", SSH: "box.example"}
			other := Host{Name: "other", SSH: "other.example"}
			if err := SaveHosts(t.Context(), []Host{h, other}); err != nil {
				t.Fatal(err)
			}
			document, err := os.ReadFile(hostsPath())
			if err != nil {
				t.Fatal(err)
			}
			next := []Host{h, other}
			if stale {
				if err := SaveHosts(t.Context(), next); err != nil {
					t.Fatal(err)
				}
			} else {
				next[1].SSH = "changed.example"
				document = []byte(strings.ReplaceAll(string(document), "other.example", "changed.example"))
			}
			before, err := os.ReadFile(hostsPath())
			if err != nil {
				t.Fatal(err)
			}
			if err := writeHostPolicyDocumentContext(t.Context(), h, next, document); err == nil {
				t.Fatal("a raw policy document replaced an unrelated or newer Source")
			}
			if got, err := os.ReadFile(hostsPath()); err != nil || !bytes.Equal(got, before) {
				t.Fatal("a refused policy document changed saved bytes")
			}
		})
	}
}

func TestHostPolicyEditorCarriesOnlyExactOriginalMarker(t *testing.T) {
	original := []byte(membershipGenerationPrefix + strings.Repeat("a", 32) + "\n# original editor comment\n")
	edited := []byte("# retained edited body\nfuture = { key = 'value' }\n")
	got, err := hostPolicyDocumentWithSourceContext(t.Context(), original, edited)
	if err != nil || !bytes.Equal(got, append(bytes.Clone(original[:bytes.IndexByte(original, '\n')+1]), edited...)) {
		t.Fatalf("editor carrier changed original marker/body: %q, %v", got, err)
	}
	for _, marker := range []string{membershipGenerationPrefix + strings.Repeat("b", 32), "# membership-generation-v2: future", "# membership-generation: malformed"} {
		if _, err := hostPolicyDocumentWithSourceContext(t.Context(), original, []byte(marker+"\n")); err == nil {
			t.Fatalf("editor promoted another marker: %s", marker)
		}
	}
}

func TestHostPolicyDocumentCannotUpgradeOwnPublication(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx, unlock, err := LockHost(t.Context(), h.Name, "first publication")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, unpin, err := holdHostPublicationContext(ctx, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer unpin()
	if err := writeHostPolicyDocumentContext(ctx, h, []Host{h}, document); err == nil {
		t.Fatal("a policy mutation upgraded its own active publication")
	}
	if got, err := os.ReadFile(hostsPath()); err != nil || !bytes.Equal(got, document) {
		t.Fatal("a refused publication upgrade changed saved bytes")
	}
}
