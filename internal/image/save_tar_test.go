package image

import (
	"bytes"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

func savedTarPlan(t *testing.T) SavedRootPlan {
	t.Helper()
	plan, err := NewSavedRootPlan(files.StatePlan{Files: []string{".agent/history"}}, files.StatePlan{Files: []string{".agent/history"}, Dirs: []string{".cache/tool"}}, "/usr/local/bin:/usr/bin:/bin", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestSavedRootIndependentlyExcludesLoginDecoysStateCachesAndPackageFiles(t *testing.T) {
	plan := savedTarPlan(t)
	entries := []flatFixture{{Name: "usr/local/bin/tool", Body: "tool", Mode: 0755}, {Name: "home/agent/.codex/auth.json", Body: "login"}, {Name: "home/agent/.agent/history", Body: "state"}, {Name: "home/agent/.agent/history-wal", Body: "sidecar"}, {Name: "home/agent/.cache/tool/archive", Body: "cache"}, {Name: "root/.ssh/id_ed25519", Body: "credential"}, {Name: "home/agent/.env.production", Body: "environment"}, {Name: "home/agent/.env.example", Body: "example"}, {Name: "usr/local/share/.wh.literal", Body: "ordinary root filename"}, {Name: "alias-to-login", Type: "hardlink", Link: "home/agent/.codex/auth.json"}}
	decoys, err := proto.DecoyPaths()
	if err != nil {
		t.Fatal(err)
	}
	packages, err := proto.PackageManagerPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(decoys, packages...) {
		entries = append(entries, flatFixture{Name: SaveHome[1:] + "/" + name, Body: "generated"})
	}
	var output bytes.Buffer
	report, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, entries...)), new(flatMemory), &output)
	if err != nil {
		t.Fatal(err)
	}
	_, result := flatResult(t, output.Bytes())
	for _, name := range []string{"usr/local/bin/tool", "home/agent/.env.example", "usr/local/share/.wh.literal"} {
		if _, present := result[name]; !present {
			t.Fatal("a retained ordinary root file was removed", name)
		}
	}
	for _, entry := range entries {
		if entry.Name == "usr/local/bin/tool" || entry.Name == "home/agent/.env.example" || entry.Name == "usr/local/share/.wh.literal" {
			continue
		}
		if _, present := result[entry.Name]; present {
			t.Fatal("an excluded path reached the normalised root", entry.Name)
		}
	}
	if len(report.Excluded) == 0 {
		t.Fatal("the review lost its exclusion reasons")
	}
}

func TestSavedRootScanDoesNotTrustChangesListOrExposeASecretFilename(t *testing.T) {
	plan := savedTarPlan(t)
	secret := api.Secret("current-value-1234567890")
	plan.Values = map[string]api.Secret{"TOKEN": secret}
	for _, entry := range []flatFixture{{Name: "etc/unreported.conf", Body: string(secret)}, {Name: "etc/" + string(secret), Body: "ordinary"}} {
		var output bytes.Buffer
		_, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, entry)), new(flatMemory), &output)
		var detail *proto.Error
		if !errors.As(err, &detail) || detail.Code != "image_secret" || strings.Contains(err.Error(), string(secret)) || output.Len() != 0 {
			t.Fatal("unreported bytes or metadata escaped the independent scan")
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("xy" + string(secret) + "tail"))
	var output bytes.Buffer
	_, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, flatFixture{Name: "etc/" + encoded, Body: "ordinary"})), new(flatMemory), &output)
	if err == nil || strings.Contains(err.Error(), string(secret)) || strings.Contains(err.Error(), encoded) || output.Len() != 0 {
		t.Fatal("an encoded secret filename escaped through a public diagnostic")
	}
	plan.OriginalState = files.StatePlan{Files: []string{".agent/" + string(secret)}}
	plan.ActualState = plan.OriginalState
	_, err = NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t)), new(flatMemory), &output)
	if err == nil || strings.Contains(err.Error(), string(secret)) || output.Len() != 0 {
		t.Fatal("a predeclared excluded filename escaped through public review metadata")
	}
}

func TestAdvisoryOmissionDoesNotWeakenWholeStreamExclusionsOrScan(t *testing.T) {
	plan := savedTarPlan(t)
	plan.Changes = []string{"/srv/listed-before-stream"}
	// These entries arrive after the immutable advisory vector was collected.
	entries := []flatFixture{{Name: "srv/listed-before-stream", Body: "ordinary"}, {Name: "etc/profile.d/added-after-list.sh", Body: "new startup code"}, {Name: "home/agent/.codex/auth.json", Body: "unreported login"}}
	var output bytes.Buffer
	report, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, entries...)), new(flatMemory), &output)
	if err != nil {
		t.Fatal(err)
	}
	_, actual := flatResult(t, output.Bytes())
	if _, present := actual["home/agent/.codex/auth.json"]; present {
		t.Fatal("an unreported excluded entry reached the image")
	}
	if _, present := actual["etc/profile.d/added-after-list.sh"]; !present || len(report.Flagged) != 0 {
		t.Fatal("the fixture invented a complete-warning guarantee for advisory changes")
	}
	secret := api.Secret("original-current-value-0123456789")
	plan.Values = map[string]api.Secret{"TOKEN": secret}
	entries = append(entries, flatFixture{Name: "etc/unreported-later.conf", Body: base64.StdEncoding.EncodeToString([]byte("x" + string(secret) + "tail"))})
	output.Reset()
	if _, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, entries...)), new(flatMemory), &output); err == nil || output.Len() != 0 {
		t.Fatal("an omitted later secret bypassed the complete independent stream scan")
	}
}

func TestSavedRootCanonicalPathCannotRevealAValueHiddenByRawSeparators(t *testing.T) {
	plan := savedTarPlan(t)
	secret := api.Secret("segment-a/segment-b-012345")
	plan.Values = map[string]api.Secret{"TOKEN": secret}
	var output bytes.Buffer
	_, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, flatFixture{Name: "etc/segment-a//segment-b-012345", Body: "ordinary"})), new(flatMemory), &output)
	var detail *proto.Error
	if !errors.As(err, &detail) || detail.Code != "image_secret" || strings.Contains(err.Error(), string(secret)) || output.Len() != 0 {
		t.Fatal("path cleaning formed a value in public metadata or a diagnostic")
	}
}

func TestSavedRootOriginalStateValidationAndReviewRemainIndependent(t *testing.T) {
	plan := savedTarPlan(t)
	plan.Changes = []string{"/etc/profile.d/tool.sh", "/usr/local/bin/tool", "/srv/setid"}
	var output bytes.Buffer
	report, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t, flatFixture{Name: "etc/profile.d/tool.sh", Body: "shell"}, flatFixture{Name: "usr/local/bin/tool", Body: "binary", Mode: 0755}, flatFixture{Name: "srv/setid", Body: "privileged", Mode: 04755})), new(flatMemory), &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"/etc/profile.d/tool.sh", "/usr/local/bin/tool", "/srv/setid"} {
		if !slices.ContainsFunc(report.Flagged, func(finding SaveFinding) bool { return finding.Path == wanted }) {
			t.Fatal("the review missed startup, PATH or privileged code", wanted)
		}
	}
	plan.OriginalState = files.StatePlan{Dirs: []string{".config"}}
	output.Reset()
	if _, err := NormaliseSavedRoot(t.Context(), plan, bytes.NewReader(flatTar(t)), new(flatMemory), &output); err == nil || output.Len() != 0 {
		t.Fatal("generated exclusions hid an invalid original state declaration")
	}
}
