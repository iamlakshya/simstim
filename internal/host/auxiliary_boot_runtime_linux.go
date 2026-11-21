//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"reflect"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/chv"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
	"github.com/iamlakshya/simstim/internal/wall/nitro"
)

type auxiliaryBootContextKeyV1 struct{}

func (boot *AuxiliaryBootV1) Context() context.Context {
	if boot == nil {
		return nil
	}
	return wall.WithExpectedSpec(boot.ctx, boot.spec)
}
func (boot *AuxiliaryBootV1) Manifest() Manifest {
	if boot == nil {
		return Manifest{}
	}
	return CloneManifest(boot.manifest)
}
func (boot *AuxiliaryBootV1) Check(ctx context.Context) error {
	if boot == nil || ctx == nil || boot.ctx == nil || boot.ctx.Err() != nil || ctx.Err() != nil || boot.candidate == nil || boot.operation == nil || boot.holder == nil || boot.domainOwner == nil {
		return auxiliaryUnavailableV1("the original new auxiliary boot owner ended")
	}
	if !boot.operation.Valid(boot.candidate.primary) || !boot.holder.Valid(boot.candidate.primary) {
		return auxiliaryUnavailableV1("the original borrowed auxiliary lease pair ended")
	}
	if boot.disk == nil || boot.diskParent == nil {
		return auxiliaryUnavailableV1("the original saved auxiliary disk ended")
	}
	if err := boot.diskParentRef.CheckRoot(boot.diskParent); err != nil {
		return err
	}
	currentParent, err := cachefs.OpenPrivateChild(boot.operation.native.root, "vm", false)
	if err != nil {
		return err
	}
	if err := errors.Join(boot.diskParentRef.CheckRoot(currentParent), currentParent.Close()); err != nil {
		return err
	}
	if err := boot.diskRef.CheckFile(boot.diskParent, microvm.SessionDiskName, boot.disk, 0600); err != nil {
		return err
	}
	if err := boot.candidate.peer.Check(); err != nil {
		return err
	}
	if err := boot.domainOwner.checkIdentity(); err != nil {
		return err
	}
	if err := checkAuxiliarySelfMembershipV1(ctx, boot.candidate.manager, boot.candidate.primary, boot.reservation.Declared); err != nil {
		return err
	}
	current, err := boot.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Auxiliary, &boot.reservation) || !bytes.Equal(current.VMNeedV1, boot.manifest.VMNeedV1) || !reflect.DeepEqual(current.RuntimeManifest, boot.manifest.RuntimeManifest) {
		return auxiliaryUnavailableV1("the original auxiliary protected source changed")
	}
	return ctx.Err()
}

// Build creates only this actual private source's recorded microvm, under its
// NEW native capped auxiliary helper. The caller cannot choose a factory,
// role, path, expected plan or callback. References are checked, never written.
func (boot *AuxiliaryBootV1) build(ctx context.Context) (wall.Sandbox, error) {
	if err := boot.Check(ctx); err != nil {
		return nil, err
	}
	boot.prepareMu.Lock()
	if boot.engine != nil || boot.buildUsed {
		boot.prepareMu.Unlock()
		return nil, auxiliaryUnavailableV1("the actual auxiliary resource factory was already consumed")
	}
	boot.buildUsed = true
	boot.prepareMu.Unlock()
	domain := func(ctx context.Context) (cachefs.Domain, error) {
		if err := boot.Check(ctx); err != nil {
			return cachefs.Domain{}, err
		}
		return boot.domainOwner.domain(), nil
	}
	facts := func(ctx context.Context) (api.CapProbe, error) {
		if err := boot.Check(ctx); err != nil {
			return api.CapProbe{}, err
		}
		return microVMFacts(ctx)
	}
	reference := func(ctx context.Context, spec wall.Spec) error {
		if err := boot.Check(ctx); err != nil {
			return err
		}
		expected, known := wall.ExpectedSpec(boot.Context())
		if !known || !reflect.DeepEqual(spec, expected) {
			return auxiliaryUnavailableV1("the actual auxiliary factory changed its captured expected source")
		}
		// Protected Need already retains every selected artifact/image. No ordinary
		// Run, Life, namespace or state generation is published by this operation.
		return nil
	}
	var driver wall.Driver
	var err error
	switch boot.need.Driver {
	case "chv":
		driver, err = chv.New(chv.Options{Facts: facts, Identity: microVMIdentity, Domain: domain, Reference: reference, Diagnostic: recordVMDiagnostic})
	case "firecracker":
		driver, err = fc.New(fc.Options{Facts: facts, Identity: microVMIdentity, Domain: domain, Reference: reference, Diagnostic: recordVMDiagnostic})
	case "nitro":
		driver, err = nitro.New(nitro.Options{Facts: facts, Identity: microVMIdentity, Domain: domain, Reference: reference, Diagnostic: recordVMDiagnostic})
	default:
		return nil, auxiliaryUnavailableV1("the original auxiliary Need names an unavailable native runtime")
	}
	if err != nil {
		return nil, err
	}
	engine, err := driver.Open(boot.Context(), boot.spec.SessionDir)
	if engine != nil {
		boot.prepareMu.Lock()
		boot.engine = engine
		boot.prepareMu.Unlock()
	}
	return engine, err
}

func (boot *AuxiliaryBootV1) prepareCorrelation(ctx context.Context, binding *microvm.NativeBindingV1, connection net.Conn) (*proto.VMPrepareCorrelationV1, error) {
	if err := boot.Check(ctx); err != nil {
		return nil, err
	}
	if binding == nil || connection == nil || binding.SessionID() != boot.candidate.primary {
		return nil, auxiliaryUnavailableV1("the auxiliary prepare has no actual same-engine native connection")
	}
	boot.prepareMu.Lock()
	defer boot.prepareMu.Unlock()
	if boot.prepareUsed || boot.engine == nil {
		return nil, auxiliaryUnavailableV1("the original auxiliary prepare was consumed or has no actual engine")
	}
	boot.prepareUsed = true
	if err := binding.Check(ctx, connection, boot.need); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	boot.correlation = proto.VMPrepareCorrelationV1{Version: 1, Session: boot.candidate.primary, Generation: boot.reservation.Token, NeedSHA256: startupDigest(boot.manifest.VMNeedV1), Nonce: hex.EncodeToString(nonce[:])}
	boot.binding, boot.connection = binding, connection
	copy := boot.correlation
	return &copy, nil
}
func (boot *AuxiliaryBootV1) ConfirmPrepared(ctx context.Context, connection net.Conn, echo *proto.VMPrepareCorrelationV1) error {
	if err := boot.Check(ctx); err != nil {
		return err
	}
	boot.prepareMu.Lock()
	defer boot.prepareMu.Unlock()
	if !boot.prepareUsed || boot.confirmed || boot.binding == nil || echo == nil || *echo != boot.correlation {
		return auxiliaryUnavailableV1("the auxiliary guest prepare did not echo its original physical continuation")
	}
	if err := boot.binding.Check(ctx, boot.connection, boot.need); err != nil {
		return err
	}
	if err := boot.binding.Check(ctx, connection, boot.need); err != nil {
		return err
	}
	boot.confirmed = true
	return nil
}

// Close joins the actual engine before either borrowed OP/holder lease can
// close. An uncertain native wait retains all source handles and caller stack.
func (boot *AuxiliaryBootV1) Close() error {
	if boot == nil {
		return nil
	}
	boot.prepareMu.Lock()
	engine := boot.engine
	boot.prepareMu.Unlock()
	if engine != nil && boot.started {
		killErr := engine.Kill()
		exit := engine.Wait()
		physical, ok := engine.(interface{ PhysicalExitV1() bool })
		if !ok {
			select {}
		}
		for !physical.PhysicalExitV1() {
			time.Sleep(100 * time.Millisecond)
		}
		_ = killErr
		_ = exit // Exit status cannot substitute for actual native closure.
	}
	if engine != nil {
		if release, ok := engine.(interface{ ReleasePreparedV1() error }); ok {
			if err := release.ReleasePreparedV1(); err != nil {
				return err
			}
		}
	}
	boot.once.Do(func() {
		boot.cancel()
		if boot.disk != nil {
			boot.closeErr = errors.Join(boot.closeErr, boot.disk.Close())
		}
		if boot.diskParent != nil {
			boot.closeErr = errors.Join(boot.closeErr, boot.diskParent.Close())
		}
		if boot.candidate != nil {
			boot.closeErr = errors.Join(boot.closeErr, boot.candidate.Close())
		}
		if boot.operation != nil {
			boot.closeErr = errors.Join(boot.closeErr, boot.operation.CloseWithError())
		}
		if boot.holder != nil {
			boot.closeErr = errors.Join(boot.closeErr, boot.holder.CloseWithError())
		}
	})
	return boot.closeErr
}

func (boot *AuxiliaryBootV1) ManagerCloseDiagnosticsV4() error {
	if boot == nil || boot.candidate == nil {
		return nil
	}
	return boot.candidate.CloseDiagnosticsV4()
}
