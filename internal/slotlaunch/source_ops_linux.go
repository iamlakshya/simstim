//go:build linux

package slotlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
)

func fixedLauncher() (*os.File, error) {
	root, err := openOwnedRoot("/usr/local/libexec/"+brand.Name, 0)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b := &backend{root: root, owner: 0}
	file, err := b.open(brand.LaunchBinary, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	st, err := file.Stat()
	if err != nil || !singleRegular(st, 0) || st.Mode().Perm() != 0o755 {
		file.Close()
		return nil, refused("the installed root launcher is not a protected executable")
	}
	return file, nil
}

// StoreNativeV1 streams only an already-held regular file's exact bytes. The
// caller retains original Need/source authority; this function grants none.
func StoreNativeV1(ctx context.Context, file *os.File, digest string, size int64) error {
	r := Request{Op: "store", SHA256: digest, Size: size}
	if err := r.validate(); err != nil {
		return err
	}
	if file == nil {
		return refused("the original store source descriptor is absent")
	}
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != size {
		return refused("the held store source has the wrong type or size")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(r); err != nil {
		return err
	}
	_, err = runJSONNativeV1(ctx, io.MultiReader(&input, io.LimitReader(file, size)))
	_, seekErr := file.Seek(0, 0)
	return errors.Join(err, seekErr)
}

func runJSONNativeV1(ctx context.Context, input io.Reader) (Response, error) {
	launcher, err := fixedLauncher()
	if err != nil {
		return Response{}, err
	}
	defer launcher.Close()
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", LauncherPath())
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	command.Stdin = input
	var output boundedResponse
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return Response{}, refused("the fixed root launcher operation failed")
	}
	var response Response
	dec := json.NewDecoder(bytes.NewReader(output.data))
	dec.DisallowUnknownFields()
	if dec.Decode(&response) != nil || dec.Decode(new(any)) != io.EOF || !response.OK {
		return Response{}, refused("the root launcher refused its operation")
	}
	return response, nil
}

// CopyNativeV1 operates only through the fixed root protocol. Root checks
// both session owners and the destination's native lifetime under its locks.
func CopyNativeV1(ctx context.Context, from, to string) error {
	request := Request{Op: "copy", From: from, To: to}
	if err := request.validate(); err != nil {
		return err
	}
	var input bytes.Buffer
	_ = json.NewEncoder(&input).Encode(request)
	_, err := runJSONNativeV1(ctx, &input)
	return err
}

// DestroyNativeV1 performs the protected session's fixed kill/destroy path.
// The caller still needs its original host operation; this creates no source.
func DestroyNativeV1(ctx context.Context, session string) error {
	request := Request{Op: "destroy", Session: session}
	if err := request.validate(); err != nil {
		return err
	}
	var input bytes.Buffer
	_ = json.NewEncoder(&input).Encode(request)
	_, err := runJSONNativeV1(ctx, &input)
	return err
}

// SlotsNativeV1 reports actual kernel locks and native quarantines through
// the same installed optional launcher. Admission still reserves atomically.
func SlotsNativeV1(ctx context.Context) ([]Slot, error) {
	var input bytes.Buffer
	_ = json.NewEncoder(&input).Encode(Request{Op: "slots"})
	response, err := runJSONNativeV1(ctx, &input)
	return response.Slots, err
}

type boundedResponse struct{ data []byte }

func (b *boundedResponse) Write(data []byte) (int, error) {
	if len(b.data) > RequestMax || len(data) > RequestMax-len(b.data) {
		return 0, refused("the launcher response exceeds its bound")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

func (s *NativeSourceV1) Stop(ctx context.Context) error {
	if s == nil {
		return refused("the native source is absent")
	}
	if !s.adopted {
		if s.attempt == nil {
			return refused("the actual native start attempt is absent")
		}
		return s.attempt.stopActors()
	}
	s.mu.Lock()
	reservation := s.reserved
	s.mu.Unlock()
	if s.PhysicalExitV1() {
		return nil
	}
	if reservation != nil {
		return reservation.stop(ctx)
	}
	s.mu.Lock()
	lease := s.pendingLease
	roles := append([]*os.File(nil), s.files...)
	s.mu.Unlock()
	if lease != nil {
		roles = append(roles, lease)
		operation, err := RunReservedCheckpointOperationV2(ctx, roles, CheckpointOperationV1{Session: s.request.Session, Action: "cleanup"})
		if operation != nil {
			err = errors.Join(err, operation.Close())
		}
		return err
	}
	var input bytes.Buffer
	_ = json.NewEncoder(&input).Encode(Request{Op: "kill", Session: s.request.Session})
	_, err := runJSONNativeV1(ctx, &input)
	return err
}
func (s *NativeSourceV1) Signal(signal os.Signal) error {
	if signal != os.Kill {
		return refused("the slot source accepts only its fixed stop operation")
	}
	return s.Stop(context.Background())
}
