//go:build linux

package chv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A valid authenticated request fits MaxFrame. Re-encoding its strings can
// expand one byte to six JSON escape bytes;4096 covers the fixed closed-plan
// envelope and bounded host socket paths. This adds no item/count cap.
const helperLimit = 6*proto.MaxFrame + 4096

type diskPlan struct {
	Version int    `json:"version"`
	Dir     string `json:"dir"`
	Bytes   int64  `json:"bytes"`
	Mkfs    string `json:"mkfs"`
	UIDMode string `json:"uid_mode"`
}
type diskReply struct {
	Disk  DiskInfo `json:"disk"`
	Error string   `json:"error,omitempty"`
}

// RunDiskHelper is the closed formatter entry point. The caller must start it
// in the canonical formatter jail under the same identity as the final VMM.
func RunDiskHelper(in io.Reader, out io.Writer) error {
	if err := helperNoDump(); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(in, helperLimit+1))
	if err != nil {
		return fmt.Errorf("read VM disk preparation: %w", err)
	}
	if len(data) > helperLimit {
		return fmt.Errorf("VM disk preparation exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan diskPlan
	if err := decoder.Decode(&plan); err != nil {
		return fmt.Errorf("decode VM disk preparation: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("VM disk preparation has trailing content")
	}
	if plan.Version != 1 {
		return fmt.Errorf("unknown VM disk preparation version")
	}
	if err := helperUID(plan.UIDMode); err != nil {
		return err
	}
	info, err := EnsureDisk(context.Background(), plan.Dir, plan.Bytes, func(ctx context.Context, file *os.File) error { return FormatDisk(ctx, plan.Mkfs, file) })
	reply := diskReply{Disk: info}
	if err != nil {
		reply.Error = err.Error()
	}
	return json.NewEncoder(out).Encode(reply)
}

func runJailJSON(ctx context.Context, spec jail.Spec, input any) ([]byte, error) {
	command, err := jail.Command(ctx, spec)
	if err != nil {
		return nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open VM helper output: %w", err)
	}
	defer reader.Close()
	defer writer.Close()
	command.Stdout = writer
	command.Stderr = writer
	if err := command.Start(); err != nil {
		return nil, err
	}
	writer.Close()
	type result struct {
		data []byte
		err  error
	}
	read := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, helperLimit+1))
		reader.Close()
		read <- result{data, err}
	}()
	writeErr := json.NewEncoder(stdin).Encode(input)
	stdin.Close()
	waitErr := command.Wait()
	output := <-read
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if writeErr != nil {
		return nil, fmt.Errorf("send VM helper plan: %w", writeErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("confined VM helper failed: %w: %s", waitErr, string(output.data))
	}
	if output.err != nil {
		return nil, fmt.Errorf("read VM helper result: %w", output.err)
	}
	if len(output.data) > helperLimit {
		return nil, fmt.Errorf("VM helper result exceeds its bound")
	}
	return output.data, nil
}

func (d *Driver) prepareDisk(ctx context.Context, specDir string, size int64, subuid bool, helper *os.File) (DiskInfo, error) {
	mkfs, binds, err := formatterBinds(d.options.Mkfs)
	if err != nil {
		return DiskInfo{}, err
	}
	binds = append(binds, jail.Bind{Path: specDir, Write: true})
	mode := "login"
	if subuid {
		mode = "subuid"
	}
	plan := diskPlan{Version: 1, Dir: specDir, Bytes: size, Mkfs: mkfs, UIDMode: mode}
	data, err := runJailJSON(ctx, jail.Spec{Argv: []string{"/proc/self/fd/3", "chv-disk"}, Net: jail.NetNone, Seccomp: jail.ProfileConverter, Subuid: subuid, Binds: binds, Files: []*os.File{helper}}, plan)
	if err != nil {
		return DiskInfo{}, err
	}
	var reply diskReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return DiskInfo{}, fmt.Errorf("decode VM disk result: %w", err)
	}
	if reply.Error != "" {
		return DiskInfo{}, fmt.Errorf("prepare session disk: %s", reply.Error)
	}
	if reply.Disk.Bytes != size {
		return DiskInfo{}, fmt.Errorf("VM disk result changed the requested disk size")
	}
	return reply.Disk, nil
}
