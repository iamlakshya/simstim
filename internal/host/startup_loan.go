package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

const (
	startupLoanOperationV1             = "startup_loan_v1"
	startupLoanPurposeSandboxPrepareV1 = "sandbox-prepare"
)

type sandboxPrepareBootV1 struct {
	operation *SessionOperation
}

// startupLoanV1 is bounded correlation, never authority. The actual outside
// launch, current native sender and same held OP descriptor supply authority.
type startupLoanV1 struct {
	Version   int    `json:"version"`
	Primary   string `json:"primary"`
	Nonce     string `json:"nonce"`
	Purpose   string `json:"purpose"`
	Need      string `json:"need"`
	Runtime   string `json:"runtime"`
	Automatic string `json:"automatic,omitempty"`
	// PrepareParent narrows only a genuine private prepare loan. It is a
	// comparison with its issuer's actual parent, never cleanup authority.
	PrepareParent *cachefs.Reference `json:"prepare_parent,omitempty"`
}

type originalBootV1 struct {
	placement       *placement // actual admitted process owner; cleanup never resolves a replacement
	policyNext      []byte     // immutable protected next-input capsule captured under SAME original OP
	gvisorIssued    bool       // guarded by owner.mu; one physical userspace-kernel start per accepted loan
	owner           *OwnedSupervisorLease
	operation       *SessionOperation
	line            *StartupLineV1
	loan            startupLoanV1
	need            []byte
	currentNeed     []byte
	runtime         *supply.Manifest
	ready           bool
	readyRequest    uint64
	readyReplied    bool
	slotReservation *originalSlotStartupV2
}

func startupRuntimeBytes(m *Manifest) ([]byte, error) {
	if m.RuntimeManifest == nil {
		return nil, nil
	}
	return json.Marshal(m.RuntimeManifest)
}

// AcceptStartupLoan accepts only the native data retained by this line's most
// recent decoded startup request. The supervisor calls it before runtime work.
// A generic operation, saved plan, FD number or copied envelope cannot call an
// alternate constructor. An accepted borrower never modifies the shared lock.
func (owner *OwnedSupervisorLease) AcceptStartupLoan(ctx context.Context, line *StartupLineV1, frame proto.Frame) (_ *SessionOperation, failure error) {
	defer func() {
		if failure != nil && line != nil {
			failure = errors.Join(failure, line.RejectAncillary())
		}
	}()
	if owner == nil || line == nil || ctx == nil {
		return nil, sessionLeaseError("startup has no original supervisor and connection")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.native == nil || !owner.Valid(owner.native.id) || owner.startup != nil {
		return nil, sessionLeaseError("this supervisor cannot accept another startup loan")
	}
	var request proto.Request
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, proto.Request{}) || json.Unmarshal(frame.Payload, &request) != nil ||
		request.Op != startupLoanOperationV1 || request.ID == 0 || len(request.Args) > 1024 || !api.ExactJSONFields(request.Args, startupLoanV1{}) {
		return nil, errors.Join(sessionLeaseError("the startup loan is not an exact supported request"), line.RejectAncillary())
	}
	var loan startupLoanV1
	if err := json.Unmarshal(request.Args, &loan); err != nil {
		return nil, errors.Join(err, line.RejectAncillary())
	}
	if loan.Version != 1 || loan.Primary != owner.native.id || len(loan.Nonce) != 64 || !supply.IsHex64(loan.Nonce) ||
		loan.Purpose != "ordinary" && loan.Purpose != "automatic" && loan.Purpose != startupLoanPurposeSandboxPrepareV1 ||
		loan.Need != "" && !supply.IsHex64(loan.Need) || loan.Runtime != "" && !supply.IsHex64(loan.Runtime) {
		return nil, errors.Join(sessionLeaseError("the startup loan has invalid correlation"), line.RejectAncillary())
	}
	if loan.Purpose == startupLoanPurposeSandboxPrepareV1 {
		if loan.PrepareParent == nil || !loan.PrepareParent.Known() || !loan.PrepareParent.Directory {
			return nil, errors.Join(sessionLeaseError("the private preparation loan has no original parent comparison"), line.RejectAncillary())
		}
	} else if loan.PrepareParent != nil {
		return nil, errors.Join(sessionLeaseError("an ordinary startup acquired another purpose's parent comparison"), line.RejectAncillary())
	}
	file, sender, err := line.takeLoanFile()
	if err != nil {
		return nil, err
	}
	var native *sessionFileLease
	defer func() {
		if failure != nil {
			if native != nil {
				failure = errors.Join(failure, native.Close())
			} else {
				failure = errors.Join(failure, file.Close())
			}
		}
	}()
	// This invokes the real kernel peer/current-sender check, including a
	// live pidfd bracket on supported pre-SO_PEERPIDFD Linux kernels.
	line.mu.Lock()
	candidate := line.candidate
	validCandidate := !line.closed && candidate != nil && candidate.owner == owner && candidate.line == line && candidate.nonce == loan.Nonce
	line.mu.Unlock()
	if !validCandidate || line.nativePeer == nil || candidate.peer != line.nativePeer {
		return nil, sessionLeaseError("the loan is not the response to this original candidate startup connection")
	}
	if err := candidate.peer.CheckSender(sender); err != nil {
		return nil, err
	}
	peerOwner := candidate.peer
	if err := checkBorrowedFlockV1(file, sender); err != nil {
		return nil, err
	}
	native, err = adoptStartupOperationFile(ctx, loan.Primary, file)
	if err != nil {
		return nil, err
	}
	operation := sessionOperationFromNative(owner.Lifetime(), loan.Primary, native)
	m, err := operation.LoadContext(ctx)
	if err != nil {
		operation.Close()
		return nil, err
	}
	runtimeBytes, err := startupRuntimeBytes(m)
	if err != nil || m.Wall == nil || m.Status.State != StateStarting || startupDigest(m.VMNeedV1) != loan.Need || startupDigest(runtimeBytes) != loan.Runtime {
		operation.Close()
		return nil, errors.Join(sessionLeaseError("the startup loan differs from the protected primary"), err)
	}
	switch loan.Purpose {
	case "automatic":
		if IsSandboxPrepareManifestV1(m) || !supply.IsHex64(loan.Automatic) || startupDigest(m.AutomaticRestore) != loan.Automatic {
			operation.Close()
			return nil, automaticRestoreError()
		}
		if _, err := ValidateAutomaticRestore(m); err != nil {
			operation.Close()
			return nil, err
		}
		operation.automaticBoot = &AutomaticBootOperationV1{operation: operation, plan: bytes.Clone(m.AutomaticRestore)}
	case startupLoanPurposeSandboxPrepareV1:
		if loan.Automatic != "" || len(m.AutomaticRestore) != 0 {
			operation.Close()
			return nil, sessionLeaseError("the sandbox-prepare startup loan cannot carry automatic restore metadata")
		}
		if err := validateSandboxPrepareBootManifestV1(m); err != nil {
			operation.Close()
			return nil, err
		}
		operation.sandboxPrepareBoot = &sandboxPrepareBootV1{operation: operation}
	default:
		if loan.Automatic != "" || len(m.AutomaticRestore) != 0 {
			operation.Close()
			return nil, automaticRestoreError()
		}
		if IsSandboxPrepareManifestV1(m) {
			operation.Close()
			return nil, sessionLeaseError("an ordinary startup loan cannot launch a fixed sandbox-prepare record")
		}
	}
	if err := errors.Join(peerOwner.Check(), checkBorrowedFlockV1(file, sender), native.check(ctx), ctx.Err()); err != nil {
		operation.Close()
		return nil, err
	}
	placement, err := admittedBootPlacementV1(ctx)
	if err != nil {
		operation.Close()
		return nil, err
	}
	boot := &originalBootV1{placement: placement, policyNext: bytes.Clone(m.PolicyNext), owner: owner, operation: operation, line: line, loan: loan, need: bytes.Clone(m.VMNeedV1), currentNeed: bytes.Clone(m.VMNeedV1)}
	if m.RuntimeManifest != nil {
		clone := *m.RuntimeManifest
		clone.Items = append([]supply.Item(nil), clone.Items...)
		clone.Advisories = append([]supply.Advisory(nil), clone.Advisories...)
		boot.runtime = &clone
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.candidate != candidate || owner.pending != nil {
		operation.Close()
		return nil, sessionLeaseError("the original startup candidate ended before its loan committed")
	}
	if err := candidate.peer.CheckSender(sender); err != nil {
		operation.Close()
		return nil, err
	}
	owner.pending, owner.startup = candidate, boot
	operation.originalBoot = boot
	if operation.IsSandboxPrepareBootV1() {
		// Only this genuinely accepted private purpose resumes interrupted
		// control frames on the SAME physical reader. Ordinary OP1/native3
		// roles and all other startup consumers retain their existing limits.
		line.prepareCursor = &sandboxPrepareFrameCursorV1{}
	}
	return operation, nil
}

func adoptStartupOperationFile(ctx context.Context, id string, file *os.File) (*sessionFileLease, error) {
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(id), false)
	if err != nil {
		return nil, err
	}
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	fileRef, err := cachefs.CaptureFile(root, lockFile, file, 0o600)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: fileRef,
		id: id, path: paths.SessionDir(id), name: lockFile, borrowed: true}
	if err := lease.check(ctx); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return lease, nil
}
