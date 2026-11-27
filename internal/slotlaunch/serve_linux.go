//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/version"
	"golang.org/x/sys/unix"
)

// Response is one bounded JSON line. PID is correlation only; later stops
// use the live keeper's actual native owner, never that numeric process ID.
type Response struct {
	native                *nativeTransfer
	OK                    bool            `json:"ok"`
	Error                 string          `json:"error,omitempty"`
	Code                  string          `json:"code,omitempty"`
	Hint                  string          `json:"hint,omitempty"`
	Slot                  *int            `json:"slot,omitempty"`
	PID                   int             `json:"pid,omitempty"`
	ExitCode              *int            `json:"exit_code,omitempty"`
	ExitSignal            string          `json:"exit_signal,omitempty"`
	Slots                 []Slot          `json:"slots,omitempty"`
	Removed               []string        `json:"removed,omitempty"`
	Checkpoints           []string        `json:"checkpoints,omitempty"`
	CheckpointTransaction json.RawMessage `json:"checkpoint_transaction,omitempty"`
	CheckpointStage       string          `json:"checkpoint_stage,omitempty"`
}

type Slot struct {
	Number      int    `json:"slot"`
	Free        bool   `json:"free"`
	Session     string `json:"session,omitempty"`
	UID         int    `json:"uid,omitempty"`
	SlotUID     int    `json:"slot_uid,omitempty"`
	Quarantined bool   `json:"quarantined,omitempty"`
}

// Serve is the root entrypoint. Identity and paths are captured here from
// native state; callers cannot replace them through an exported constructor.
func Serve(ctx context.Context, input io.Reader, output io.Writer) int {
	response, err := serve(ctx, input)
	if err != nil {
		owned, transaction := response.native, response.CheckpointTransaction
		response = Response{native: owned, CheckpointTransaction: transaction, OK: false, Code: "launcher_refused", Error: err.Error(), Hint: "Run the host doctor and check its separate-account installation."}
		var known *launchError
		if errors.As(err, &known) {
			response.Code, response.Error, response.Hint = known.Code, known.Message, known.Hint
		}
	}
	if response.native != nil {
		defer response.native.close()
		if nativeSocket(output) {
			if sendNativeResponse(response) != nil {
				response.native.reject()
				return 1
			}
			if err != nil {
				return 1
			}
			return 0
		}
	}
	if json.NewEncoder(output).Encode(response) != nil {
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}

func serve(ctx context.Context, input io.Reader) (Response, error) {
	who, err := nativeCaller()
	if err != nil {
		return Response{}, err
	}
	request, remaining, err := ReadRequest(input)
	if err != nil {
		return Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if request.Op == "update" {
		return Response{OK: true}, update(request, remaining)
	}
	b, err := openBackend(who)
	if err != nil {
		return Response{}, err
	}
	defer b.root.Close()
	defer b.guard.Close()
	response := Response{OK: true}
	switch request.Op {
	case "store":
		err = b.store(request, remaining)
	case "start":
		return b.start(ctx, request)
	case "start.fork", "start.reserved-v2":
		return b.startWithNativeInputsV2(ctx, request)
	case "native":
		return b.inspectGuest(ctx, request)
	case "checkpoint":
		return b.inspectGuest(ctx, request)
	case "checkpoint.reserve-v2":
		return b.inspectGuest(ctx, request)
	case "checkpoint.stop-v2":
		return b.stopCheckpointReservationV2(ctx, request)
	case "reserved.cleanup-v2":
		return b.cleanupReservedStartV2(ctx, request)
	case "reserved.destroy-v2":
		return b.cleanupReservedStartV2(ctx, request)
	case "reserved.abandon-v2":
		return b.abandonReservedStartV2(ctx, request)
	case "checkpoint.stopped-v2":
		return b.stoppedCheckpointOperationV2(ctx, request)
	case "checkpoint.prepare-v2", "checkpoint.exchange-v2", "checkpoint.rollback-v2", "checkpoint.finalize-v2":
		return b.replaceCheckpointReservedV2(ctx, request)
	case "checkpoint.read":
		return b.inspectCheckpointReadV1(ctx, request)
	case "checkpoint.discard-v2":
		return b.inspectCheckpointReadV1(ctx, request)
	case "checkpoint.space":
		return b.inspectCheckpointSpaceV1(ctx, request)
	case "kill":
		err = b.kill(ctx, request.Session)
	case "destroy":
		err = b.destroy(ctx, request.Session)
	case "copy":
		err = b.copyDisk(request.From, request.To)
	case "prune":
		response.Removed, err = b.prune(request.Keep)
	case "slots":
		response.Slots, err = b.slots()
	default:
		err = refused("the launcher operation has no implementation")
	}
	return response, err
}

func update(request Request, input io.Reader) error {
	metadata := make([]byte, request.MetaSize)
	signature := make([]byte, request.SigSize)
	if _, err := io.ReadFull(input, metadata); err != nil {
		return refused("the launcher update envelope is incomplete")
	}
	if _, err := io.ReadFull(input, signature); err != nil {
		return refused("the launcher update signature is incomplete")
	}
	envelope, err := VerifyEnvelope(metadata, signature)
	if err != nil {
		return err
	}
	if err := newerEnvelope(envelope, version.Version); err != nil {
		return err
	}
	root, err := openOwnedRoot(filepath.Dir(LauncherPath()), 0)
	if err != nil {
		return err
	}
	defer root.Close()
	b := &backend{root: root, owner: 0}
	lock, err := b.lock(".update.lock", false)
	if err != nil {
		return err
	}
	defer lock.Close()
	installed, err := b.open("launch", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	st, err := installed.Stat()
	installed.Close()
	if err != nil || !singleRegular(st, 0) || st.Mode().Perm() != 0o755 {
		return refused("the installed launcher is not a protected executable")
	}
	// The lock spans version checking and replacement. A concurrent older
	// invocation cannot replace a newly installed launcher: compare its
	// actual executable inode with the locked installation as well.
	current, err := os.Stat("/proc/self/exe")
	if err != nil || !os.SameFile(st, current) {
		return refused("the launcher changed before its update lock was acquired")
	}
	tmp, err := temporary("launch")
	if err != nil {
		return err
	}
	file, err := b.open(tmp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	err = checkPayload(file, input, request.Size, envelope.SHA256)
	if err == nil {
		err = checkExecutable(file, envelope.Arch)
	}
	if err == nil {
		err = file.Chmod(0o755)
	}
	if err == nil {
		err = file.Sync()
	}
	err = joinClose(err, file)
	if err != nil {
		return err
	}
	if err := root.Rename(tmp, "launch"); err != nil {
		return err
	}
	return b.syncDir(".")
}

func (b *backend) slots() ([]Slot, error) {
	lock, err := b.lock("store/.lock", false)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	defer func() { _ = b.refreshCapacityLocked() }()
	return b.slotInventory()
}

func (b *backend) slotInventory() ([]Slot, error) {
	accounts, err := slotAccounts()
	if err != nil {
		return nil, err
	}
	out := make([]Slot, 0, SlotCount)
	for i := range SlotCount {
		lock, err := b.lock(slotLockName(i), true)
		free := err == nil
		if err != nil && !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		if lock != nil {
			lock.Close()
		}
		item := Slot{Number: i, Free: free, SlotUID: accounts[i].uid}
		if free {
			if err := b.vacancy(i); err != nil {
				item.Free = false
				item.Quarantined = true
			}
		}
		if !free {
			var record slotRecord
			if err := b.readJSON(slotRecordName(i), &record); err != nil {
				return nil, err
			}
			if record.Slot != i || record.UID <= 0 || record.SlotUID <= 0 {
				return nil, refused("a live slot record is invalid")
			}
			// Other users get capacity only, never another account's IDs.
			if record.UID == b.caller.uid {
				item.Session, item.UID, item.SlotUID = record.Session, record.UID, record.SlotUID
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func slotLockName(slot int) string   { return "slots/" + twoDigits(slot) + ".lock" }
func slotRecordName(slot int) string { return "slots/" + twoDigits(slot) + ".json" }
func twoDigits(slot int) string      { return string([]byte{'0' + byte(slot/10), '0' + byte(slot%10)}) }
