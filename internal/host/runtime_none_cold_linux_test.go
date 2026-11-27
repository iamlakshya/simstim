//go:build linux

package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func requireManagerlessNoneFixtureV1(t *testing.T) {
	t.Helper()
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/systemd/private", os.Getuid()))
	if !errors.Is(err, os.ErrNotExist) {
		t.Skip("this native fixture requires an actual managerless Linux host; it never changes a live user's manager")
	}
}

func TestNoneActualStopColdResumeStartsANewNativeRun(t *testing.T) {
	requireManagerlessNoneFixtureV1(t)
	hosttest.New(t)
	startUnrelatedNoneND0FixtureV1(t)
	s := start(t, "none-cold", "/bin/sh", "-c", "printf NONE-COLD; exec sleep 60")
	other := start(t, "other-none", "/bin/sh", "-c", "printf OTHER-NONE; exec sleep 60")
	waitOutput(t, s.ID, "NONE-COLD")
	waitOutput(t, other.ID, "OTHER-NONE")
	op, err := host.BeginSessionOperation(context.Background(), s.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := host.StopNoneSessionForOperationV1(context.Background(), op)
	if err != nil {
		op.Close()
		t.Fatal(err)
	}
	if err := stopped.Check(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	prior, err := host.LoadManifest(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prior.NoneCleanup == nil || prior.NoneCleanup.Generation != prior.Run.Generation || prior.NoneCleanup.InitBirth == "" || prior.Life == nil || prior.Life.Phase != host.LifeStopped {
		t.Fatal("the original runtime did not durably join its real cleanup")
	}
	if err := stopped.Close(); err != nil {
		t.Fatal(err)
	}
	if err := op.CloseWithError(); err != nil {
		t.Fatal(err)
	}
	b := newRawBridge(t)
	prepared := b.request(50, "resume.prepare-v1", api.ResumePrepareArgsV1{V: 1, Start: api.ResumeArgs{ID: s.ID}})
	if !prepared.OK {
		t.Fatal("the actual same-boot None source could not prepare", prepared.Error)
	}
	var plan api.ResumePrepareResultV1
	if err := json.Unmarshal(prepared.Result, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Prepared == nil || plan.Mode != "cold" {
		t.Fatal("the stopped original controller did not select its separate cold source")
	}
	result := b.request(51, "resume", api.ResumeArgs{ID: s.ID, Prepared: plan.Prepared})
	if !result.OK {
		t.Fatal("the actual same connection could not consume its original None source", result.Error)
	}
	current, err := host.LoadManifest(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.NoneCleanup != nil || current.Run.Generation == prior.Run.Generation || current.Run.Generation == "" || !host.IsNoneSupervisor(current) {
		t.Fatal("the new native run replayed the previous cleanup epoch")
	}
	waitOutput(t, s.ID, "NONE-COLD")
	b.attach(s.ID, -1, 80, 24)
	if got := b.output(0, "NONE-COLD"); !bytes.Equal(got, []byte("NONE-COLD")) {
		t.Fatalf("resumed None attach changed bytes: %q", got)
	}
	if live(t, other.ID).Run.Generation == "" {
		t.Fatal("another healthy ND0 session was disturbed")
	}
}

func TestNonePreparedCancelKeepsExactCanonicalCleanupAndConnectionOwnership(t *testing.T) {
	requireManagerlessNoneFixtureV1(t)
	hosttest.New(t)
	s := start(t, "none-cancel", "/bin/sh", "-c", "printf NONE-CANCEL; exec sleep 60")
	waitOutput(t, s.ID, "NONE-CANCEL")
	op, err := host.BeginSessionOperation(context.Background(), s.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := host.StopNoneSessionForOperationV1(context.Background(), op)
	if err != nil {
		op.Close()
		t.Fatal(err)
	}
	if err := stopped.Close(); err != nil {
		op.Close()
		t.Fatal(err)
	}
	if err := op.CloseWithError(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.SessionDir(s.ID), "manifest.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b := newRawBridge(t)
	prepared := b.request(60, "resume.prepare-v1", api.ResumePrepareArgsV1{V: 1, Start: api.ResumeArgs{ID: s.ID}})
	if !prepared.OK {
		t.Fatal(prepared.Error)
	}
	var plan api.ResumePrepareResultV1
	if err := json.Unmarshal(prepared.Result, &plan); err != nil {
		t.Fatal(err)
	}
	foreign := newRawBridge(t)
	if response := foreign.request(61, "resume.prepared.check-v1", plan.Prepared); response.OK {
		t.Fatal("another accepted connection reconstructed the original None source from its token")
	}
	if response := b.request(62, "resume.prepared.cancel-v1", plan.Prepared); !response.OK {
		t.Fatal(response.Error)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("pre-dispatch cancellation changed the original canonical cleanup")
	}
	if response := b.request(63, "resume.prepared.check-v1", plan.Prepared); response.OK {
		t.Fatal("a cancelled continuation remained reusable")
	}
}
