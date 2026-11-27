package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSavedRootDescriptorExporterExcludesPrivateStateAndDoesNotFollowLinks(t *testing.T) {
	directory := t.TempDir()
	for name, body := range map[string]string{"usr/local/bin/tool": "ordinary executable", "home/agent/.codex/auth.json": "login", "home/agent/.bunfig.toml": "generated", "home/agent/.cache/tool/archive": "cache", "root/.ssh/id_ed25519": "credential", "usr/local/share/.wh.literal": "literal filename"} {
		full := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "value"), []byte("outside-body-must-not-be-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link")); err != nil {
		t.Skipf("native link fixture unavailable: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan, err := NewSavedRootPlan(files.StatePlan{}, files.StatePlan{Dirs: []string{".cache/tool"}}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteSavedRoot(t.Context(), root, plan, &output); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, output.Bytes())
	for _, name := range []string{"usr/local/bin/tool", "usr/local/share/.wh.literal", "link"} {
		if _, present := entries[name]; !present {
			t.Fatal("an ordinary root entry was lost", name)
		}
	}
	for _, name := range []string{"home/agent/.codex/auth.json", "home/agent/.bunfig.toml", "home/agent/.cache/tool/archive", "root/.ssh/id_ed25519", "link/value"} {
		if _, present := entries[name]; present {
			t.Fatal("a private or traversed entry was exported", name)
		}
	}
	if bytes.Contains(output.Bytes(), []byte("outside-body-must-not-be-read")) {
		t.Fatal("a source link was followed for content")
	}
	output.Reset()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WriteSavedRoot(ctx, root, plan, &output); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatal("cancelled export wrote a partial root", err)
	}
}

func TestSavedUpperListingIsBoundedAndNeverFollowsDirectoryLinks(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "etc/profile"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "link")); err != nil {
		t.Skipf("native link fixture unavailable: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	paths, err := ListSavedChanges(t.Context(), root, proto.ImageChangesMax)
	if err != nil || !reflect.DeepEqual(paths, []string{"/etc", "/etc/profile", "/link"}) {
		t.Fatal("the advisory listing changed paths or followed a link", err)
	}
	if _, err := ListSavedChanges(t.Context(), root, 1); err == nil {
		t.Fatal("a caller changed the fixed advisory count contract")
	}
}

func TestSavedReviewCoalescesAllReasonsWithoutMultiplyingThePathBudget(t *testing.T) {
	plan := savedTarPlan(t)
	modes := make(map[string]int64, proto.ImageChangesMax)
	for index := 0; index < proto.ImageChangesMax; index++ {
		name := "/usr/local/bin/" + strings.Repeat("x", 48) + string(rune('a'+index/10000)) + strings.Repeat("q", index%3)
		// Include a unique decimal suffix while keeping all raw paths below8MiB.
		name += fmt.Sprintf("-%06d", index)
		plan.Changes = append(plan.Changes, name)
		modes[name] = 04755
	}
	filter, err := newSavedRootFilter(plan)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := filter.review(plan.Changes, modes)
	if err != nil || len(findings) != proto.ImageChangesMax {
		t.Fatal("overlapping canonical rules duplicated or dropped paths", err)
	}
	for _, finding := range findings {
		if !strings.Contains(finding.Reason, "image PATH") || !strings.Contains(finding.Reason, "set-user-ID") {
			t.Fatal("coalescing discarded a canonical reason")
		}
	}
	review := SaveReview{ID: strings.Repeat("a", 32), Ref: "tools", Size: 4096, Flagged: findings}
	if err := review.Validate("tools"); err != nil {
		t.Fatal("multiple reasons multiplied the declared path budget", err)
	}
}
