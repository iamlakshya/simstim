package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func hostLabelState(t *testing.T) client.Host {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	host := client.Host{Name: "box", SSH: "box.example", Labels: map[string]string{"kept": "yes"}}
	if err := client.SaveHosts(t.Context(), []client.Host{host}); err != nil {
		t.Fatal(err)
	}
	return host
}

func TestHostSetLabelsUsesTheExistingLocalTransaction(t *testing.T) {
	before := hostLabelState(t)
	var out bytes.Buffer
	if err := setHost(t.Context(), &out, "box", []string{"label.location=home", "label.owner=tools"}); err != nil {
		t.Fatal(err)
	}
	got, err := client.FindHost("box")
	if err != nil || got.Labels["location"] != "home" || got.Labels["owner"] != "tools" || got.Labels["kept"] != "yes" {
		t.Fatal("a label edit changed or lost another label", got.Labels, err)
	}
	withoutLabels := got
	withoutLabels.Labels = before.Labels
	if !client.SameEntry(withoutLabels, before) {
		t.Fatal("a local note changed another saved host setting")
	}
	if err := setHost(t.Context(), &out, "box", []string{"label.location="}); err != nil {
		t.Fatal(err)
	}
	got, err = client.FindHost("box")
	if err != nil || got.Labels["owner"] != "tools" || got.Labels["kept"] != "yes" {
		t.Fatal("removing a label changed the remaining notes", got.Labels, err)
	}
	if _, found := got.Labels["location"]; found {
		t.Fatal("an empty value was saved instead of removing the label")
	}
}

func TestHostSetLabelsChecksEveryPairBeforeSaving(t *testing.T) {
	for _, test := range []struct {
		name  string
		words []string
	}{
		{"duplicate", []string{"label.location=home", "label.location=office"}},
		{"missing key", []string{"label.location=home", "label.=invalid"}},
		{"unsafe value", []string{"label.location=home", "label.unsafe=value\x1bc"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := hostLabelState(t)
			var out bytes.Buffer
			if err := setHost(t.Context(), &out, "box", test.words); err == nil {
				t.Fatal("an invalid label transaction succeeded")
			}
			after, err := client.FindHost("box")
			if err != nil || !client.SameEntry(before, after) || out.Len() != 0 {
				t.Fatalf("a rejected transaction changed or reported a partial label edit: err=%v out=%q", err, out.String())
			}
		})
	}
}

func TestHostLabelBindingOwnsItsMapAndCleansPreviousValues(t *testing.T) {
	original := client.Host{Name: "box", Labels: map[string]string{"location": "old\x1bc", "kept": "yes"}}
	changed := original
	setting := findSetting("label.location")
	if setting == nil || setting.save == nil || setting.apply != nil {
		t.Fatal("labels bypass the saved-host transaction or require a host operation")
	}
	if strings.ContainsRune(setting.now(original), '\x1b') {
		t.Fatal("a previous saved value could write terminal controls")
	}
	setting.save(&changed, "home")
	if original.Labels["location"] != "old\x1bc" || changed.Labels["location"] != "home" || changed.Labels["kept"] != "yes" {
		t.Fatal("a label factory borrowed the saved entry's mutable map")
	}
	if findHostLabelSetting(nil, "label.location") != nil {
		t.Fatal("an unregistered family could change a host")
	}
	if bindHostLabelSetting("tier") != nil {
		t.Fatal("the label factory bound another setting family")
	}
}

func TestHostSetLabelCancellationLeavesSavedStateUntouched(t *testing.T) {
	before := hostLabelState(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := setHost(ctx, &out, "box", []string{"label.location=home"}); !errors.Is(err, context.Canceled) {
		t.Fatal("label editing discarded cancellation", err)
	}
	after, err := client.FindHost("box")
	if err != nil || !client.SameEntry(before, after) || out.Len() != 0 {
		t.Fatal("cancelled label editing changed saved state", err, out.String())
	}
}

func TestHostLsLabelsAreDisplayedAndJSONKeepsMetadata(t *testing.T) {
	withFake(t, nil)
	host := hostLabelState(t)
	// The current host list also reads health. Route that read through the
	// existing in-memory provider, never through a real SSH target.
	host.SSH = ""
	host.Machine = &client.Machine{Provider: "fake", ID: "fake-box"}
	if err := saveHost(host); err != nil {
		t.Fatal(err)
	}
	if err := setHost(t.Context(), &bytes.Buffer{}, "box", []string{"label.location=home"}); err != nil {
		t.Fatal(err)
	}
	var runErr error
	text, _ := captureOutput(t, func() {
		cmd := hostLsCmd()
		cmd.SetContext(t.Context())
		cmd.SetArgs(nil)
		runErr = cmd.Execute()
	})
	if runErr != nil || !strings.Contains(text, "kept=yes") || !strings.Contains(text, "location=home") || strings.ContainsRune(text, '\x1b') {
		t.Fatal("host ls did not display safe metadata", text, runErr)
	}
	raw, _ := captureOutput(t, func() {
		cmd := hostLsCmd()
		cmd.SetContext(t.Context())
		cmd.SetArgs([]string{"--json"})
		runErr = cmd.Execute()
	})
	var rows []struct {
		Labels map[string]string
	}
	if err := json.Unmarshal([]byte(raw), &rows); runErr != nil || err != nil || len(rows) != 1 || rows[0].Labels["location"] != "home" || rows[0].Labels["kept"] != "yes" {
		t.Fatal("labels were missing from machine output", raw, runErr, err)
	}
}
