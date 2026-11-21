package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

type rootAccountStageReceipt struct {
	Version int    `json:"version"`
	UID     int    `json:"uid"`
	Size    int64  `json:"size"`
	Device  string `json:"device"`
	Inode   string `json:"inode"`
}
type rootAccountStagePhase uint8

const (
	rootAccountStage rootAccountStagePhase = iota + 1
	rootAccountVerify
	rootAccountInstalled
)

type rootAccountOutput struct{ data []byte }

func (b *rootAccountOutput) Write(data []byte) (int, error) {
	if len(b.data) > 4096 || len(data) > 4096-len(b.data) {
		return 0, rootStepError("the launcher staging response exceeds its bound")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

type rootAccountProcess struct {
	owner        *rootAccountPreparation
	command      *exec.Cmd
	finishNative func() error
	once         sync.Once
	err          error
}

func (p *rootAccountProcess) Wait() error {
	p.once.Do(func() {
		p.err = errors.Join(p.command.Wait(), p.finishNative())
		p.owner.mu.Lock()
		p.owner.process = nil
		p.owner.mu.Unlock()
	})
	return p.err
}
func (p *rootAccountProcess) Kill() {
	if p != nil && p.command != nil && p.command.Process != nil {
		_ = p.command.Process.Kill()
	}
}

func (p *rootAccountPreparation) runStage(ctx context.Context, verify bool) error {
	phase := rootAccountStage
	if verify {
		phase = rootAccountVerify
	}
	process, output, err := p.startStage(ctx, phase)
	if err != nil {
		return err
	}
	if err := waitRootStepProcess(ctx, process); err != nil {
		return errors.Join(rootStepError("the original public launcher staging process did not finish"), ctx.Err(), err)
	}
	var receipt rootAccountStageReceipt
	if !api.ExactJSONFields(output.data, receipt) {
		return rootStepError("the launcher staging receipt has unknown or repeated fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(output.data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF || receipt.Version != 1 || receipt.UID != p.uid || receipt.Size != p.item.Size || !rootAccountDecimal(receipt.Device) || !rootAccountDecimal(receipt.Inode) {
		return rootStepError("the actual launcher staging process returned no complete checked receipt")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return grantOperationError()
	}
	if verify {
		if !p.staged || !reflect.DeepEqual(p.receipt, receipt) {
			return rootStepError("the original staged launcher changed before root execution")
		}
	} else {
		p.receipt, p.staged = receipt, true
	}
	return nil
}

func rootAccountDecimal(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (p *rootAccountPreparation) startStage(ctx context.Context, phase rootAccountStagePhase) (_ rootStepOwnedProcess, _ *rootAccountOutput, returned error) {
	if p == nil || p.conn == nil || p.request != grantRequestOf(ctx) {
		return nil, nil, grantPurposeError()
	}
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return nil, nil, err
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return nil, nil, err
	}
	operation, err := p.request.borrowNewSettings(ctx)
	if err != nil {
		return nil, nil, err
	}
	probe, err := p.conn.CapProbe(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	if probe.OS != "linux" || probe.User != p.user || probe.UID != p.uid || probe.Arch != p.arch {
		return nil, nil, rootStepError("the actual host account or architecture changed before launcher staging")
	}
	ssh, ok := p.conn.route.t.(*SSH)
	if !ok || ssh.Host.SSH == "" {
		return nil, nil, rootStepError("launcher staging needs the same original SSH route")
	}
	transport := *ssh
	transport.Interactive = false
	native, finishNative, err := transport.beginNativeSource(ctx)
	if err != nil {
		return nil, nil, err
	}
	ownedNative := true
	defer func() {
		if ownedNative {
			returned = errors.Join(returned, finishNative())
		}
	}()
	owned, release, err := p.conn.beginLocalGrant(native, grantLocalRoot, grantOpRootStep, "stage original public launcher")
	if err != nil {
		return nil, nil, err
	}
	defer release()
	source, err := captureHostStateSourceWitness(owned, p.conn)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	if p.closed || p.starting || p.process != nil || phase != rootAccountStage && !p.staged || phase == rootAccountStage && p.staged || phase != rootAccountStage && phase != rootAccountVerify && phase != rootAccountInstalled {
		p.mu.Unlock()
		return nil, nil, grantOperationError()
	}
	if p.source.Version == 0 {
		p.source = source
	} else if !sameReceiptSource(p.source, source) {
		p.mu.Unlock()
		return nil, nil, grantOperationError()
	}
	p.starting = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.starting = false; p.mu.Unlock() }()
	if err := p.source.check(owned, p.conn); err != nil {
		return nil, nil, err
	}
	_, hid, err := grantHostTier(owned, p.conn.Host)
	if err != nil {
		return nil, nil, err
	}
	if hid != p.conn.HostID() {
		return nil, nil, grantIdentityError(p.conn.Host)
	}
	text, err := p.stageText(phase)
	if err != nil {
		return nil, nil, err
	}
	command, err := transport.command(owned, native, "/bin/sh -c "+ShellQuote(text), "-T")
	if err != nil {
		return nil, nil, err
	}
	if phase == rootAccountStage {
		command.Stdin = bytes.NewReader(p.data)
	}
	output := &rootAccountOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	command.WaitDelay = sshWaitDelay
	if err := operation.Revalidate(owned); err != nil {
		return nil, nil, err
	}
	if err := p.source.check(owned, p.conn); err != nil {
		return nil, nil, err
	}
	if err := transport.checkNativeSourceScope(owned); err != nil {
		return nil, nil, err
	}
	if err := requireLocalOperation(owned, grantLocalRoot, grantOpRootStep); err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, nil, grantOperationError()
	}
	if err := command.Start(); err != nil {
		p.mu.Unlock()
		return nil, nil, err
	}
	process := &rootAccountProcess{owner: p, command: command, finishNative: finishNative}
	p.process = process
	p.mu.Unlock()
	ownedNative = false
	release() // Native Start published; no terminal, byte stream or Wait holds HostOP.
	return process, output, nil
}

func (p *rootAccountPreparation) stageText(phase rootAccountStagePhase) (string, error) {
	name := "stage-launcher"
	if phase == rootAccountInstalled {
		name = "installed-launcher"
	}
	data, err := rootTemplates.ReadFile("rootstep/" + name + ".sh")
	if err != nil {
		return "", err
	}
	check := "0"
	device, inode := "0", "0"
	if phase != rootAccountStage {
		check = "1"
		device, inode = p.receipt.Device, p.receipt.Inode
	}
	return strings.NewReplacer("@BRAND@", brand.Name, "@UID@", strconv.Itoa(p.uid), "@LAUNCH_SHA256@", p.item.SHA256, "@LAUNCH_SIZE@", strconv.FormatInt(p.item.Size, 10), "@VERIFY@", check, "@DEVICE@", device, "@INODE@", inode).Replace(string(data)), nil
}
