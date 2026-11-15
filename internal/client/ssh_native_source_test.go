package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

// This oracle tests scope/lifetime composition only. It is not a native Lima
// instance, accepted SSH peer or provider-source authority fixture.
type nativeSSHSourceFixture struct {
	opens, checks, closes atomic.Int32
	fail                  error
}
type nativeSSHLeaseFixture struct {
	owner  *nativeSSHSourceFixture
	ctx    context.Context
	closed atomic.Bool
}

func (f *nativeSSHSourceFixture) OpenSSHNativeSource(ctx context.Context) (SSHNativeSourceLease, error) {
	f.opens.Add(1)
	return &nativeSSHLeaseFixture{owner: f, ctx: ctx}, nil
}
func (f *nativeSSHLeaseFixture) Revalidate(ctx context.Context) error {
	f.owner.checks.Add(1)
	if f.closed.Load() {
		return errors.New("native source already closed")
	}
	if err := f.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.owner.fail
}
func (f *nativeSSHLeaseFixture) Close() error {
	if f.closed.CompareAndSwap(false, true) {
		f.owner.closes.Add(1)
	}
	return nil
}

func TestSSHNativeRestrictionSharesOneLeaseAndNeverReopensEndedScope(t *testing.T) {
	factory := &nativeSSHSourceFixture{}
	original := &SSH{Host: Host{Name: "box", SSH: "generated.example"}}
	transport, err := original.WithNativeSource(factory)
	if err != nil || original.nativeSource != nil || transport == original {
		t.Fatal("native restriction mutated the original transport", err)
	}
	if err := transport.checkNativeSourceScope(t.Context()); err == nil {
		t.Fatal("raw command builder acquired native lifetime from transport alone")
	}
	ctx, finish, err := transport.beginNativeSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, nestedFinish, err := transport.beginNativeSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := nestedFinish(); err != nil {
		t.Fatal(err)
	}
	if factory.opens.Load() != 1 || factory.closes.Load() != 0 {
		t.Fatal("nested config read closed or reopened the actual outer lease")
	}
	changed := *transport
	changed.Host.SSHConfig = "different-generated-config"
	if err := changed.checkNativeSourceScope(ctx); err == nil {
		t.Fatal("native scope donated its pin to a changed route")
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := transport.beginNativeSource(context.WithoutCancel(ctx)); err == nil || factory.opens.Load() != 1 || factory.closes.Load() != 1 {
		t.Fatal("ended native scope was refreshed or reused", err)
	}
}

func TestSSHNativeRestrictionReportsFinalSourceFailureAndCloses(t *testing.T) {
	factory := &nativeSSHSourceFixture{}
	transport, err := (&SSH{Host: Host{Name: "box", SSH: "generated.example"}}).WithNativeSource(factory)
	if err != nil {
		t.Fatal(err)
	}
	_, finish, err := transport.beginNativeSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("generated native source replacement")
	factory.fail = failure
	if err := finish(); !errors.Is(err, failure) || factory.closes.Load() != 1 {
		t.Fatal("final source failure was lost or lease was not closed", err)
	}
	if err := finish(); !errors.Is(err, failure) || factory.closes.Load() != 1 {
		t.Fatal("repeated finish changed the original resource result", err)
	}
}

func TestLimaHomeReservationUsesOriginalCompleteNamespace(t *testing.T) {
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-run")
	body := "[secrets]\nLIMA_HOME=" + strconv.Quote(secretSettingsMarkerCommand(marker)) + "\n"
	if err := os.WriteFile(settingsPath(), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIMA_HOME", "ambient-locator-must-not-be-read-or-emitted")
	metadata, err := OpenProviderSourceMetadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	if err := metadata.ReserveLimaHome(t.Context()); err == nil {
		t.Fatal("derived locator key bypassed the canonical configured namespace")
	}
	requireSecretSettingsMarkerAbsent(t, marker)
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nOTHER='unused'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Environment(t.Context()); err == nil {
		t.Fatal("reservation failure refreshed the original settings source")
	}
}
