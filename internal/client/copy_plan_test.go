package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func copyFixture(t *testing.T, directory, name, data string) string {
	t.Helper()
	file := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCopyPlanPinsBytesAndRejectsChangedPublication(t *testing.T) {
	dir := t.TempDir()
	name := copyFixture(t, dir, "input", "one")
	plan, err := buildCopyPlan(context.Background(), []CopySource{{Host: name, Guest: "/opt/input"}}, dir, api.DefaultCopyMax)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err = plan.files[0].write(context.Background(), &got); err != nil || got.String() != "one" {
		t.Fatalf("copy = %q, %v", got.String(), err)
	}
	if err = os.WriteFile(name, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = plan.files[0].write(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("changed bytes accepted")
	}
}

func TestCopyPlanSensitiveAliasAndContainedLinks(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir, "tree/data", "safe")
	if err := os.Symlink("data", filepath.Join(dir, "tree/link")); err != nil {
		t.Skip(err)
	}
	plan, err := buildCopyPlan(context.Background(), []CopySource{{Host: filepath.Join(dir, "tree"), Guest: "/opt/tree"}}, dir, api.DefaultCopyMax)
	if err != nil || len(plan.files) != 2 {
		t.Fatalf("contained alias: %v", err)
	}
	copyFixture(t, dir, "tree/.env", "never upload")
	if _, err = buildCopyPlan(context.Background(), []CopySource{{Host: filepath.Join(dir, "tree"), Guest: "/opt/tree"}}, dir, api.DefaultCopyMax); err == nil {
		t.Fatal("sensitive directory entry accepted")
	}
	if err = os.Remove(filepath.Join(dir, "tree/.env")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("../outside", filepath.Join(dir, "tree/escape")); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, dir, "outside", "outside grant")
	if _, err = buildCopyPlan(context.Background(), []CopySource{{Host: filepath.Join(dir, "tree"), Guest: "/opt/tree"}}, dir, api.DefaultCopyMax); err == nil {
		t.Fatal("escaping alias accepted")
	}
}

func TestCopyPlanCapsCollisionsAndCancellationBeforeRead(t *testing.T) {
	dir := t.TempDir()
	first := copyFixture(t, dir, "first", "four")
	second := copyFixture(t, dir, "second", "five")
	if _, err := buildCopyPlan(context.Background(), []CopySource{{Host: first, Guest: "/opt/a"}, {Host: second, Guest: "/opt/b"}}, dir, 7); err == nil {
		t.Fatal("aggregate byte cap ignored")
	}
	if _, err := buildCopyPlan(context.Background(), []CopySource{{Host: first, Guest: "/opt/a"}, {Host: second, Guest: "/opt/a"}}, dir, 100); err == nil {
		t.Fatal("duplicate destination accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildCopyPlan(ctx, []CopySource{{Host: filepath.Join(dir, "absent")}}, dir, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestCopyPlanRootReplacementIsRefused(t *testing.T) {
	dir := t.TempDir()
	source := copyFixture(t, dir, "chosen/input", "original")
	plan, err := buildCopyPlan(context.Background(), []CopySource{{Host: source, Guest: "/opt/a"}}, dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(dir, "chosen"), filepath.Join(dir, "old")); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, dir, "chosen/input", "attacker")
	var out bytes.Buffer
	if err = plan.files[0].write(context.Background(), &out); err == nil || out.Len() != 0 {
		t.Fatalf("root replacement wrote %d bytes, %v", out.Len(), err)
	}
}
