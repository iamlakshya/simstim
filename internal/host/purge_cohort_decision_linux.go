//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type purgeCohortValueV3 uint8

const (
	purgeCohortPendingV3 purgeCohortValueV3 = iota
	purgeCohortCommitV3
	purgeCohortAbortV3
)

// PurgeCohortCommitByteV3 is the fixed first-packet commit value. Receiving
// these bytes outside the actual original socketpair supplies no authority.
const PurgeCohortCommitByteV3 byte = byte(purgeCohortCommitV3)

// PurgeCohortAbortByteV3 is the fixed first-packet abort value.
const PurgeCohortAbortByteV3 byte = byte(purgeCohortAbortV3)

// PurgeCohortDecisionV3 owns one actual first-queued-decision kernel pair.
// It is not a stopped/session source. Only its SAME transaction can issue
// original-OP loans; a commit additionally needs the genuine global permit.
type PurgeCohortDecisionV3 struct {
	mu                           sync.Mutex
	transaction                  *purgeActiveTransactionV2
	receiver, sender             *os.File
	receiverInfo, senderInfo     os.FileInfo
	receiverCookie, senderCookie uint64
	loans                        map[*PurgeCohortDecisionLoanV3]struct{}
	closed                       bool
}

// PurgeCohortDecisionLoanV3 is a resource-only projection of the actual pair.
// Its original OP and native files cannot be selected by a DTO or caller FD.
type PurgeCohortDecisionLoanV3 struct {
	mu               sync.Mutex
	owner            *PurgeCohortDecisionV3
	operation        *SessionOperation
	receiver, sender *os.File
	closed           bool
}

func newPurgeCohortDecisionV3(transaction *purgeActiveTransactionV2) (result *PurgeCohortDecisionV3, returned error) {
	if transaction == nil || transaction.lifetime == nil || transaction.closed || transaction.aborted {
		return nil, placementError("the native cohort decision has no SAME original live transaction")
	}
	if err := errors.Join(transaction.lifetime.Err(), CheckCaller()); err != nil {
		return nil, err
	}
	owner := &PurgeCohortDecisionV3{transaction: transaction, loans: map[*PurgeCohortDecisionLoanV3]struct{}{}}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.closeUnusedV3()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	owner.receiver = os.NewFile(uintptr(pair[0]), "actual-purge-cohort-first-decision-receiver")
	owner.sender = os.NewFile(uintptr(pair[1]), "actual-purge-cohort-first-decision-sender")
	if err := unix.SetsockoptInt(pair[1], unix.SOL_SOCKET, unix.SO_SNDBUF, 4096); err != nil {
		return nil, err
	}
	owner.receiverInfo, err = owner.receiver.Stat()
	if err != nil {
		return nil, err
	}
	owner.senderInfo, err = owner.sender.Stat()
	if err != nil {
		return nil, err
	}
	owner.receiverCookie, err = unix.GetsockoptUint64(pair[0], unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		return nil, err
	}
	owner.senderCookie, err = unix.GetsockoptUint64(pair[1], unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		return nil, err
	}
	if err := owner.checkFilesV3(); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func checkPurgeDecisionFileV3(file *os.File, created os.FileInfo, cookie uint64) error {
	if file == nil || created == nil || cookie == 0 {
		return placementError("the actual cohort decision lost its original native socket")
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, created) || info.Mode()&os.ModeSocket == 0 {
		return errors.Join(placementError("the cohort decision socket differs from its original kernel object"), err)
	}
	for _, role := range []struct{ option, want int }{{unix.SO_TYPE, unix.SOCK_SEQPACKET}, {unix.SO_DOMAIN, unix.AF_UNIX}} {
		actual, err := unix.GetsockoptInt(int(file.Fd()), unix.SOL_SOCKET, role.option)
		if err != nil || actual != role.want {
			return errors.Join(placementError("the cohort decision descriptor has another native socket role"), err)
		}
	}
	actual, err := unix.GetsockoptUint64(int(file.Fd()), unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil || actual != cookie {
		return errors.Join(placementError("the cohort decision socket identity changed"), err)
	}
	return nil
}

func (owner *PurgeCohortDecisionV3) checkFilesV3() error {
	if owner == nil || owner.closed || owner.transaction == nil {
		return placementError("the original cohort decision is absent or closed")
	}
	return errors.Join(checkPurgeDecisionFileV3(owner.receiver, owner.receiverInfo, owner.receiverCookie), checkPurgeDecisionFileV3(owner.sender, owner.senderInfo, owner.senderCookie))
}

func peekPurgeCohortDecisionV3(receiver *os.File) (purgeCohortValueV3, error) {
	if receiver == nil {
		return purgeCohortPendingV3, placementError("the original native cohort receiver is absent")
	}
	var value [2]byte
	n, _, flags, _, err := unix.Recvmsg(int(receiver.Fd()), value[:], nil, unix.MSG_PEEK|unix.MSG_DONTWAIT)
	if errors.Is(err, unix.EAGAIN) {
		return purgeCohortPendingV3, nil
	}
	if err != nil {
		return purgeCohortPendingV3, err
	}
	if n != 1 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || value[0] != byte(purgeCohortCommitV3) && value[0] != byte(purgeCohortAbortV3) {
		return purgeCohortPendingV3, placementError("the actual first native cohort decision has an unknown packet shape")
	}
	return purgeCohortValueV3(value[0]), nil
}

func proposePurgeCohortDecisionV3(receiver, sender *os.File, value purgeCohortValueV3) (purgeCohortValueV3, error) {
	if sender == nil || value != purgeCohortCommitV3 && value != purgeCohortAbortV3 {
		return purgeCohortPendingV3, placementError("the native cohort proposal has another fixed value")
	}
	first, err := peekPurgeCohortDecisionV3(receiver)
	if err != nil || first != purgeCohortPendingV3 {
		return first, err
	}
	n, err := unix.SendmsgN(int(sender.Fd()), []byte{byte(value)}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil && !errors.Is(err, unix.EAGAIN) {
		return purgeCohortPendingV3, err
	}
	if err == nil && n != 1 {
		return purgeCohortPendingV3, io.ErrShortWrite
	}
	first, peekErr := peekPurgeCohortDecisionV3(receiver)
	if peekErr != nil {
		return purgeCohortPendingV3, peekErr
	}
	if first == purgeCohortPendingV3 {
		return first, placementError("the original bounded decision queue has no observable first proposal")
	}
	return first, nil
}

func (owner *PurgeCohortDecisionV3) borrowForOperationV3(ctx context.Context, operation *SessionOperation) (result *PurgeCohortDecisionLoanV3, returned error) {
	if owner == nil || ctx == nil || operation == nil {
		return nil, placementError("the cohort loan has no SAME original operation")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.checkFilesV3()); err != nil {
		return nil, err
	}
	found := false
	for _, subject := range owner.transaction.subjects {
		if subject.operation == operation && subject.manifest != nil {
			found = true
			break
		}
	}
	if !found || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, sessionLeaseError("the cohort loan is not this actual vector's original owning primary operation")
	}
	value, err := peekPurgeCohortDecisionV3(owner.receiver)
	if err != nil || value != purgeCohortPendingV3 {
		return nil, errors.Join(placementError("the decided cohort cannot lend to a new monitor"), err)
	}
	loan := &PurgeCohortDecisionLoanV3{owner: owner, operation: operation}
	owner.loans[loan] = struct{}{}
	complete := false
	defer func() {
		if !complete {
			closeErr := loan.closeFilesV3()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = loan
			} else {
				delete(owner.loans, loan)
			}
		}
	}()
	for _, pair := range []struct {
		from *os.File
		to   **os.File
	}{{owner.receiver, &loan.receiver}, {owner.sender, &loan.sender}} {
		fd, err := unix.FcntlInt(pair.from.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return nil, err
		}
		*pair.to = os.NewFile(uintptr(fd), "same-original-purge-cohort-decision-loan")
	}
	complete = true
	return loan, nil
}

// WithNativeDecisionFilesV1 projects existing RESOURCE files only. The fixed
// outside factory must already own its genuine OP/root/current-copy source;
// this callback is never a Source or commit-permission predicate.
func (loan *PurgeCohortDecisionLoanV3) WithNativeDecisionFilesV1(ctx context.Context, use func(receiver, sender *os.File) error) error {
	if loan == nil || ctx == nil || use == nil {
		return placementError("the original decision projection is absent")
	}
	loan.mu.Lock()
	defer loan.mu.Unlock()
	if loan.closed || loan.owner == nil || loan.operation == nil || !loan.operation.Valid(loan.operation.Primary()) {
		return sessionLeaseError("the original cohort projection lost its same owning operation")
	}
	owner := loan.owner
	if err := errors.Join(ctx.Err(), checkPurgeDecisionFileV3(loan.receiver, owner.receiverInfo, owner.receiverCookie), checkPurgeDecisionFileV3(loan.sender, owner.senderInfo, owner.senderCookie)); err != nil {
		return err
	}
	used := use(loan.receiver, loan.sender)
	return errors.Join(used, checkPurgeDecisionFileV3(loan.receiver, owner.receiverInfo, owner.receiverCookie), checkPurgeDecisionFileV3(loan.sender, owner.senderInfo, owner.senderCookie))
}

func (loan *PurgeCohortDecisionLoanV3) closeFilesV3() error {
	var failure error
	for _, file := range []*os.File{loan.receiver, loan.sender} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

// Close closes only this original duplicate pair after its real monitor has
// joined. It cannot decide, thaw, stop or reconstruct another resource.
func (loan *PurgeCohortDecisionLoanV3) Close() error {
	if loan == nil {
		return nil
	}
	loan.mu.Lock()
	defer loan.mu.Unlock()
	if loan.closed {
		return observeOriginalPurgeDescriptorCloseV3([]*os.File{loan.receiver, loan.sender}, nil)
	}
	failure := loan.closeFilesV3()
	observed := observeOriginalPurgeDescriptorCloseV3([]*os.File{loan.receiver, loan.sender}, nil)
	if observed == nil {
		loan.closed = true
		if loan.owner != nil {
			loan.owner.mu.Lock()
			delete(loan.owner.loans, loan)
			loan.owner.mu.Unlock()
		}
	}
	return errors.Join(failure, observed)
}

func (owner *PurgeCohortDecisionV3) closeUnusedV3() error {
	var failure error
	for _, file := range []*os.File{owner.receiver, owner.sender} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}
