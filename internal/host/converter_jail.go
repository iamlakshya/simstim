package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/publicationlease"
)

// runConverterJail is the ONE owned converter command/stdio/wait path. Source
// production begins only after actual jail Start. Both operations share the
// same bounded result, cancellation and whole-owned-jail cleanup machinery.
func runConverterJail(ctx context.Context, spec jail.Spec, produce func(io.Writer) error) ([]byte, error) {
	return runConverterJailOutput(ctx, spec, produce, func(input io.Reader) ([]byte, error) {
		data, err := io.ReadAll(io.LimitReader(input, image.MaxManifest+1))
		if int64(len(data)) > image.MaxManifest {
			return nil, fmt.Errorf("jailed converter result exceeds its bound")
		}
		return data, err
	})
}

func runConverterJailOutput(ctx context.Context, spec jail.Spec, produce func(io.Writer) error, readOutput func(io.Reader) ([]byte, error)) ([]byte, error) {
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	command, err := jail.Command(operation, spec)
	if err != nil {
		return nil, err
	}
	holder, ok := ctx.Value(publicationHolderContextV1{}).(*publicationlease.HolderV1)
	if !ok || holder == nil {
		return nil, placementError("the converter lost its original inherited publication holder")
	}
	if err := command.RetainPublicationLeaseV1(holder); err != nil {
		return nil, err
	}
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	output, writeOutput, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer output.Close()
	defer writeOutput.Close()
	command.Stdout = writeOutput
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	writeOutput.Close()
	type readResult struct {
		data []byte
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		data, err := readOutput(output)
		if err != nil {
			cancel()
		}
		output.Close()
		read <- readResult{data, err}
	}()
	wait := make(chan error, 1)
	go func() { err := command.Wait(); input.Close(); wait <- err }()
	sourceErr := produce(input)
	input.Close()
	if sourceErr != nil {
		cancel()
	}
	waitErr := <-wait
	result := <-read
	if ctx.Err() != nil {
		return result.data, ctx.Err()
	}
	if sourceErr != nil {
		return result.data, sourceErr
	}
	return result.data, errors.Join(waitErr, result.err)
}
