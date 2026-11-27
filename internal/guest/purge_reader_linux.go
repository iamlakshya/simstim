//go:build linux

package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

type purgeReaderV1 struct {
	init       *Init
	connection *vmInitConnection
	files      *vmFiles
	listener   *os.File
	accepted   net.Conn
	mu         sync.Mutex
	used       bool
	retained   bool
	failure    error
	active     *purgeReaderProbeV1
}

// This is the only reader constructor. Host-side boot authority is issued by
// the separate original-copy backend, not by this routing argument or a reply.
func runPurgeReaderVMV1() error {
	if err := verifyVMEntry(); err != nil {
		return err
	}
	if err := secretmem.Protect(); err != nil {
		return err
	}
	reader := &purgeReaderV1{files: newVMFiles("/", realVMMounts())}
	fail := func(err error) error {
		reader.failure = errors.Join(reader.failure, err)
		if reader.files.pivoted {
			if backErr := reader.files.pivotBack(); backErr != nil {
				reader.retainV1(backErr)
			}
		}
		if closeErr := reader.files.close(); closeErr != nil {
			reader.retainV1(closeErr)
		}
		return reader.failure
	}
	if err := reader.files.bootstrap(); err != nil {
		return fail(err)
	}
	if err := verifyVMToolsRoot(); err != nil {
		return fail(err)
	}
	if err := verifyPurgeReaderDisksV1(vmReadKernelFile); err != nil {
		return fail(err)
	}
	if err := verifyPurgeReaderNetworkV1(); err != nil {
		return fail(err)
	}
	if err := reader.files.layoutPurgeReaderV1(); err != nil {
		return fail(err)
	}
	if err := reader.files.pivotPurgeReaderV1(); err != nil {
		return fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer cancel()
	listener, err := listenVMGuest()
	reader.listener = listener
	if err != nil {
		reader.retainV1(err)
	}
	connection, err := acceptVMGuest(ctx, listener)
	reader.accepted = connection
	closeErr := listener.Close()
	if closeErr == nil {
		reader.listener = nil
	}
	if err != nil || closeErr != nil {
		reader.retainV1(errors.Join(err, closeErr))
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	g := New(connection, "", logger)
	reader.init = g
	reader.connection, err = newVMInitConnection(g.lifetime, g, connection)
	if err != nil {
		reader.retainV1(err)
	}
	g.purgeReader = reader
	g.reaper = becomeSubreaper(logger)
	if !g.reaper {
		reader.retainV1(purgeWorkErrorV1("the fixed reader cannot retain its original children"))
	}
	// The closed helper cannot fork before its fixed exec, and its native
	// filter forbids Git children. Do not let a census reaper consume a
	// partially started/unknown original command before this owner's Wait.
	go func() {
		select {
		case <-ctx.Done():
			g.end()
		case <-g.done:
		}
	}()
	g.Serve() // its one teardown joins every actual command before returning.
	reader.mu.Lock()
	prior := reader.failure
	reader.mu.Unlock()
	if prior != nil {
		reader.retainV1(prior)
	}
	if err := reader.files.pivotBack(); err != nil {
		reader.retainV1(err)
	}
	if err := reader.files.close(); err != nil {
		reader.retainV1(err)
	}
	if err := powerOffGuest(); err != nil {
		reader.retainV1(err)
	}
	reader.retainV1(purgeWorkErrorV1("the fixed reader power-off unexpectedly returned"))
	return nil
}

func (p *purgeReaderV1) retainV1(err error) {
	p.mu.Lock()
	first := !p.retained
	p.retained = true
	p.failure = errors.Join(p.failure, err)
	p.mu.Unlock()
	if first {
		if p.init != nil && p.init.log != nil {
			p.init.log.Error("the fixed reader retains its actual resources", "err", err)
		} else {
			fmt.Fprintln(os.Stderr, "The fixed reader retains its original resources. Try: retain its actual outside reader owner.", err)
		}
	}
	for {
		time.Sleep(time.Hour)
		runtime.KeepAlive(p) // actual partial mounts, native connection, command and files stay owned.
	}
}

func (p *purgeReaderV1) request(request proto.Request, reply func(any, error)) {
	replyDeadline := time.Now().Add(time.Minute)
	answer := func(value any, err error) {
		p.init.mu.Lock()
		stream := p.init.ctlSt
		p.init.mu.Unlock()
		if stream != nil {
			_ = stream.SetWriteDeadline(replyDeadline)
		}
		reply(value, err)
	}
	if err := purgeReaderRequestV1(request.Op, request.Args); err != nil {
		answer(nil, err)
		return
	}
	if err := p.connection.check(p.init); err != nil {
		answer(nil, purgeWorkErrorV1("the fixed reader lost its original native control connection"))
		return
	}
	p.mu.Lock()
	if p.used || p.failure != nil {
		p.mu.Unlock()
		answer(nil, purgeWorkErrorV1("the fixed reader's one original inspection is no longer available"))
		return
	}
	p.used = true
	p.mu.Unlock()
	work, err := p.init.inspectPurgeWorkV1(request.Args)
	if err != nil {
		answer(nil, purgeWorkErrorV1("the original fixed workspace inspection did not finish completely"))
		return
	}
	if err := p.connection.check(p.init); err != nil {
		answer(nil, purgeWorkErrorV1("the fixed reader's original native connection ended during inspection"))
		return
	}
	result := proto.PurgeReaderWorkResultV1{ABI: proto.GuestPurgeReaderV1, Work: work}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > proto.PurgeReaderResultLimitV1 {
		answer(nil, purgeWorkErrorV1("the fixed work observation exceeds its protected result bound"))
		return
	}
	answer(result, nil)
}
