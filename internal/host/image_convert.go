package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
)

// ConvertImage creates a memory-limited guardian and canonical converter jail.
// Programs calling it dispatch the helper's two internal image commands.
func ConvertImage(ctx context.Context, layers []image.Layer, files []*os.File, out *os.File) error {
	plan, err := json.Marshal(layers)
	if err != nil {
		return err
	}
	var completed imageGuardianFrame
	if err := convertScoped(ctx, convertOCIImage, plan, files, out, &completed); err != nil {
		return err
	}
	return imageConversionOutcome(nil, completed, nil, nil, imageOOMEvidence{}, "")
}

func convertScoped(ctx context.Context, operation converterOperation, plan []byte, files []*os.File, out *os.File, completed *imageGuardianFrame) (returned error) {
	return convertScopedWithSource(ctx, operation, plan, files, out, completed, nil, nil)
}

func convertScopedWithSource(ctx context.Context, operation converterOperation, plan []byte, files []*os.File, out *os.File, completed *imageGuardianFrame, source func(context.Context, io.Writer) error, review func([]byte) error) (returned error) {
	if (operation == convertSavedRoot) != (source != nil) || (operation == convertSavedRoot) != (review != nil) {
		return fmt.Errorf("the closed converter operation has an invalid source producer")
	}
	helper, err := os.Open("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("pin image helper: %w", err)
	}
	defer helper.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("name owned image scope: %w", err)
	}
	unit := brand.Name + "-image-" + hex.EncodeToString(nonce[:]) + ".scope"
	execution, cancel := context.WithCancel(ctx)
	defer cancel()
	arguments, err := converterArguments(operation, len(files), unit)
	if err != nil {
		return err
	}
	command := exec.CommandContext(execution, fmt.Sprintf("/proc/self/fd/%d", 3+len(files)), arguments...)
	command.ExtraFiles = append(append([]*os.File{}, files...), helper, out)
	command.WaitDelay = 5 * time.Second
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	native, err := newImageNativeGuardianV1(ctx, command, helper, unit)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, native.closeV1()) }()
	fence, err := retainPublicationHelperV1(ctx)
	if err != nil {
		return err
	}
	// This SAME parent duplicate outlives atomic creation, native descendant
	// closure and Wait, including every uncertain Start/error path below.
	defer fence.Close()
	if err := appendPublicationGuardianV1(command, fence); err != nil {
		return err
	}
	input, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open image guardian input: %w", err)
	}
	defer input.Close()
	output, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open image guardian output: %w", err)
	}
	defer output.Close()
	var diagnostic imageOutput
	command.Stderr = &diagnostic
	startErr := command.Start()
	var nativeStartErr error
	if native != nil {
		nativeStartErr = native.childStartedV1(startErr)
	}
	if startErr != nil {
		if native != nil {
			nativeStartErr = errors.Join(nativeStartErr, native.joinV1())
		}
		return errors.Join(fmt.Errorf("start image guardian: %w", startErr), nativeStartErr)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	waited := false
	var owned imageScopeInfo
	var watch *imageScopeWatch
	var frameReaderDone <-chan struct{}
	defer func() {
		input.Close()
		cancel()
		if native != nil {
			returned = errors.Join(returned, native.joinV1())
		}
		if frameReaderDone != nil {
			_ = output.Close()
			<-frameReaderDone
		}
		cleanupErr := cleanupImageScope(unit, owned)
		if !waited {
			<-wait
		}
		if watch != nil {
			watch.events.Close()
			watch.maximum.Close()
		}
		if cleanupErr != nil && ctx.Err() == nil {
			returned = errors.Join(returned, cleanupErr)
		}
	}()
	if nativeStartErr != nil {
		return nativeStartErr
	}
	var frames <-chan imageFrameResult
	if source != nil {
		frames, frameReaderDone = imageSaveGuardianFrames(execution, output)
	} else {
		frames = imageGuardianFrames(output)
	}
	ready, err := nextImageGuardianFrame(ctx, frames)
	if err != nil {
		cancel()
		waitErr := <-wait
		waited = true
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return UserError("image_scope", fmt.Sprintf("the image guardian did not enter its scope: %v; %v; %s", err, waitErr, diagnostic.String()), "Check the systemd user manager with host doctor.")
	}
	if ready.Phase != "ready" || !validImageInvocation(ready.InvocationID) {
		return UserError("image_scope", "the image guardian returned invalid scope ownership", "Upgrade the helper and run host doctor.")
	}
	owned = imageScopeInfo{InvocationID: ready.InvocationID, ControlGroup: ready.ControlGroup}
	watch, err = pinImageScope(ctx, unit, owned)
	if err != nil {
		return UserError("image_scope", err.Error(), "Check the cgroup v2 memory controller and systemd user manager with host doctor.")
	}
	if native != nil {
		if err := native.bindScopeV1(ctx, owned); err != nil {
			return err
		}
		if err := native.releasePlanV1(); err != nil {
			return err
		}
	}
	if err := sendConverterPlan(input, plan); err != nil {
		return err
	}
	var done imageGuardianFrame
	var frameErr error
	if source != nil {
		early, sourceErr := sendSavedRootStream(execution, cancel, input, frames, source)
		if sourceErr != nil {
			return sourceErr
		}
		if early != nil {
			done, frameErr = early.frame, early.err
		} else {
			done, frameErr = nextImageGuardianFrame(ctx, frames)
		}
	} else {
		done, frameErr = nextImageGuardianFrame(ctx, frames)
	}
	for frameErr == nil && done.Phase == "review" && review != nil {
		if err := review(done.Output); err != nil {
			return err
		}
		done, frameErr = nextImageGuardianFrame(ctx, frames)
	}
	var evidence imageOOMEvidence
	if ctx.Err() == nil {
		evidence = watch.outcome(ctx)
	}
	if frameErr == nil && done.Phase == "done" {
		if err := imageAcknowledge(input); err != nil {
			frameErr = err
		}
	} else {
		cancel()
	}
	input.Close()
	waitErr := <-wait
	waited = true
	if err := conversionTransportOutcome(ctx.Err(), done, frameErr, waitErr, evidence, diagnostic.String()); err != nil {
		if source != nil && ctx.Err() == nil {
			if refusal := savedRootOutcomeFailure(done.Output); refusal != nil {
				return errors.Join(refusal, err)
			}
		}
		return err
	}
	*completed = done
	return nil
}

type imageFrameResult struct {
	frame imageGuardianFrame
	err   error
}

func imageGuardianFrames(in io.Reader) <-chan imageFrameResult {
	frames := make(chan imageFrameResult, 2)
	go func() {
		defer close(frames)
		decoder := json.NewDecoder(io.LimitReader(in, imageControlLimit+1))
		decoder.DisallowUnknownFields()
		for range 2 {
			var frame imageGuardianFrame
			err := decoder.Decode(&frame)
			frames <- imageFrameResult{frame, err}
			if err != nil {
				return
			}
		}
	}()
	return frames
}
func nextImageGuardianFrame(ctx context.Context, frames <-chan imageFrameResult) (imageGuardianFrame, error) {
	select {
	case <-ctx.Done():
		return imageGuardianFrame{}, ctx.Err()
	case result, ok := <-frames:
		if !ok {
			return imageGuardianFrame{}, io.ErrUnexpectedEOF
		}
		return result.frame, result.err
	}
}

func imageConversionOutcome(cancelled error, done imageGuardianFrame, frameErr, waitErr error, evidence imageOOMEvidence, diagnostic string) error {
	if err := conversionTransportOutcome(cancelled, done, frameErr, waitErr, evidence, diagnostic); err != nil {
		return err
	}
	var response struct {
		DroppedDevices int    `json:"dropped_devices"`
		Error          string `json:"error,omitempty"`
		Code           string `json:"code,omitempty"`
	}
	if err := json.Unmarshal(done.Output, &response); err != nil {
		return UserError("image_convert", "the jailed image converter returned an invalid result: "+err.Error(), "Upgrade the helper and run host doctor.")
	}
	if response.Error != "" {
		return UserError(response.Code, response.Error, "Rebuild the image, or use a smaller one.")
	}
	if response.DroppedDevices > 0 {
		fmt.Fprintf(os.Stderr, "image converter dropped %d device nodes\n", response.DroppedDevices)
	}
	return nil
}

// exec joins each bounded diagnostic writer before its result is inspected.
type imageOutput struct{ bytes.Buffer }

func (w *imageOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := int(image.MaxManifest) - w.Len()
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		_, _ = w.Buffer.Write(p[:keep])
	}
	return n, nil
}

func conversionTransportOutcome(cancelled error, done imageGuardianFrame, frameErr, waitErr error, evidence imageOOMEvidence, diagnostic string) error {
	if cancelled != nil {
		return cancelled
	}
	if evidence.positive {
		return UserError("image_too_large", "the owned image scope recorded an OOM kill while its memory cap was 1 GiB", "Free memory on the host, or use a smaller image with fewer entries and a smaller compression window.")
	}
	if frameErr != nil || waitErr != nil || evidence.err != nil || done.Phase != "done" || done.Failure != "" {
		reasons := []string{}
		for _, err := range []error{frameErr, waitErr, evidence.err} {
			if err != nil {
				reasons = append(reasons, err.Error())
			}
		}
		for _, text := range []string{done.Failure, strings.TrimSpace(diagnostic)} {
			if text != "" {
				reasons = append(reasons, text)
			}
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "the guardian did not report its completed outcome")
		}
		return UserError("image_convert", "the jailed image conversion failed: "+strings.Join(reasons, "; "), "Check host doctor and the image's layers.")
	}
	return nil
}
