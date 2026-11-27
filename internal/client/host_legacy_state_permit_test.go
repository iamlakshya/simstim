package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func legacyStateTransportFixture(t *testing.T) (Host, context.Context, *HostStateObservation, *Conn, func()) {
	t.Helper()
	h := hostTransportSavedFixture(t)
	c := finishGeneratedTransportFixture(t, h, "", "session.id", "resume")
	ctx, o, release := transportPublicationFixture(t, h, "")
	if err := c.BindHostStateObservation(ctx); err != nil {
		t.Fatal(err)
	}
	if o.hasCheckedIdentity() || !o.hasLegacyBinding() || o.hostID != "" || o.checkedConn != nil {
		t.Fatal("unchecked transport fixture fabricated checked identity")
	}
	return h, ctx, o, c, release
}

func TestHostLegacyTransportBindingAloneGrantsNoCanonicalWrite(t *testing.T) {
	h, ctx, _, _, _ := legacyStateTransportFixture(t)
	for _, target := range []struct {
		dirs []string
		name string
	}{
		{nil, grantsFileName},
		{[]string{"hosts", HostFileKey(h.Name)}, "baseline.json"},
		{[]string{"hosts", HostFileKey(h.Name)}, "root-steps.json"},
		{[]string{"hosts", HostFileKey(h.Name)}, "sandboxes.json"},
		{[]string{"hosts", HostFileKey(h.Name)}, "removal.json"},
	} {
		called := false
		err := withStateFile(ctx, target.dirs, target.name, true, func([]byte, bool) ([]byte, error) {
			called = true
			return []byte("forbidden arbitrary write"), nil
		})
		if err == nil || called {
			t.Fatalf("the unchecked observation alone reached %s mutation", target.name)
		}
	}
	terminalAssertAbsent(t, removalDir(h.Name), filepath.Join(StateDir(), grantsFileName), filepath.Join(StateDir(), grantsFileName+".lock"))
}

func TestHostLegacyPermitWrongFileReplaySourceAndRevocationRefuseBeforeWriter(t *testing.T) {
	for _, kind := range []string{"wrong-file", "already-consumed", "source", "revoked", "wrong-observation"} {
		t.Run(kind, func(t *testing.T) {
			_, ctx, o, c, _ := legacyStateTransportFixture(t)
			// This generated permit is used ONLY for pre-semantic negative
			// guards. Main's actual New/Resume/Upload tests prove eligibility.
			permit := &legacyStateWritePermit{observation: o, op: o.op, source: o.source, target: grantsFileName,
				intent: legacyStateWriteIntent{kind: legacyNewCredentialRecord, conn: c, outbound: Outbound{Kind: OutboundSecret, Name: "TEST"}}}
			permit.active.Store(true)
			name := grantsFileName
			current := o
			switch kind {
			case "wrong-file":
				name = "root-steps.json"
			case "already-consumed":
				permit.consumed.Store(true)
			case "source":
				permit.source.generation = "00000000000000000000000000000000"
			case "revoked":
				permit.active.Store(false)
			case "wrong-observation":
				current = &HostStateObservation{}
			}
			if err := permit.consume(ctx, current, nil, name); err == nil {
				t.Fatal("an invalid/replayed exact-target permit reached semantic admission")
			}
			terminalAssertAbsent(t, filepath.Join(StateDir(), grantsFileName), filepath.Join(StateDir(), grantsFileName+".lock"))
		})
	}
}

func TestHostLegacyExpiredOriginalOPCannotAcquireReplacementWithSameToken(t *testing.T) {
	h, ctx, o, _, release := legacyStateTransportFixture(t)
	release()
	if _, err := o.legacyTransport(context.WithoutCancel(ctx)); err == nil {
		t.Fatal("ended OP retained unchecked transport authority")
	}
	if _, release, err := LockHost(WithHostStateObservation(t.Context(), o), h.Name, "old-token replacement"); err == nil {
		release()
		t.Fatal("old legacy token acquired another physical owner")
	}
	if _, err := os.Lstat(removalDir(h.Name)); !os.IsNotExist(err) {
		t.Fatalf("expired legacy token recreated state: %v", err)
	}
}

func TestHostLegacyPermitCategoriesDoNotInferOtherKindsOrReceiptAuthority(t *testing.T) {
	for _, intent := range []legacyStateWriteIntent{
		{kind: legacyNewCredentialRecord, outbound: Outbound{Kind: OutboundSSHAgent}},
		{kind: legacyNewCredentialRecord, outbound: Outbound{Kind: OutboundSSHKey, Name: "KEY"}},
		{kind: legacyResumeCredentialRecord, outbound: Outbound{Kind: OutboundAlertToken}},
		{kind: legacyPasteRecord, outbound: Outbound{Kind: OutboundPaste}},
		{kind: legacyNewNoneReceipt},
		{kind: legacyStateWriteKind(99)},
	} {
		if _, err := legacyStateWriteTarget(intent); err == nil {
			t.Fatal("an enum or missing specific proof created a legacy write category")
		}
	}
}
