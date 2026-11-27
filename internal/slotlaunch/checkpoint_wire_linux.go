//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"os"
)

// Every field is present even when zero. Request.Parse checks this closed
// action-specific envelope before the root side accepts native descriptors.
type checkpointRequestV1 struct {
	Op         string `json:"op"`
	Session    string `json:"session"`
	Action     string `json:"action"`
	Name       string `json:"name"`
	Generation string `json:"generation"`
	KeepCount  int    `json:"keep_count"`
	MetaSize   int64  `json:"meta_size"`
}

func (custody *CheckpointCustodyV1) callRunning(ctx context.Context, action, name string, keep int, payload []byte) (Response, []*os.File, error) {
	done, err := custody.begin(ctx)
	if err != nil {
		return Response{}, nil, err
	}
	defer done()
	custody.operations.Lock()
	defer custody.operations.Unlock()
	if err := custody.checkRunning(ctx); err != nil {
		return Response{}, nil, err
	}
	if custody.checkpoints == nil {
		_, files, err := custody.runningExchange(ctx, "open", "", 0, nil)
		if err != nil {
			for _, file := range files {
				file.Close()
			}
			return Response{}, nil, err
		}
		if len(files) != 1 {
			for _, file := range files {
				file.Close()
			}
			return Response{}, nil, refused("the root checkpoint capture has no original directory role")
		}
		custody.checkpoints = files[0]
	}
	response, files, err := custody.runningExchange(ctx, action, name, keep, payload)
	if err == nil {
		err = custody.checkRunning(ctx)
	}
	if err != nil {
		for _, file := range files {
			file.Close()
		}
		return Response{}, nil, err
	}
	return response, files, nil
}

func (custody *CheckpointCustodyV1) runningExchange(ctx context.Context, action, name string, keep int, payload []byte) (Response, []*os.File, error) {
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	joined := make(chan struct{})
	stop := context.AfterFunc(custody.ctx, func() { defer close(joined); cancel() })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	roles := append([]*os.File(nil), custody.source.files...)
	roles = append(roles, custody.source.parent, custody.vmPeer, custody.guest, custody.checkpoints)
	operation, err := RunCheckpointOperationV1(owned, roles, CheckpointOperationV1{Session: custody.source.request.Session, Action: action, Name: name, KeepCount: keep, Metadata: payload})
	if operation == nil {
		return Response{}, nil, err
	}
	response := operation.ResultV1()
	var files []*os.File
	{
		readErr := operation.WithFilesV1(context.WithoutCancel(owned), func(outputs []*os.File) error {
			for _, output := range outputs {
				file, err := dupFile(output)
				if err != nil {
					return err
				}
				files = append(files, file)
			}
			return nil
		})
		if err == nil {
			err = readErr
		}
	}
	err = errors.Join(err, operation.Close())
	return response, files, err
}
