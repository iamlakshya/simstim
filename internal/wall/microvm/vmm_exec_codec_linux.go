//go:build linux

package microvm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

const finalExecPlanPrefixV2 = "VMMEXEC2"
const finalExecContinueV2 = byte(0xa7)

func writeFinalExecPlanV2(writer io.Writer, plan runnerPlan) error {
	if plan.Version != 2 {
		return fmt.Errorf("direct VMM final plan is not version2")
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(data) > helperLimit-len(finalExecPlanPrefixV2)-3 {
		return fmt.Errorf("direct VMM final plan exceeds its original bound")
	}
	prefix := append([]byte(finalExecPlanPrefixV2), data...)
	prefix = append(prefix, '\n')
	n, err := writer.Write(prefix)
	if err != nil {
		return err
	}
	if n != len(prefix) {
		return io.ErrShortWrite
	}
	return nil
}

func readFinalExecPlanV1(input io.Reader) (runnerPlan, *bufio.Reader, error) {
	var plan runnerPlan
	reader := bufio.NewReaderSize(io.LimitReader(input, helperLimit+1), 4096)
	peek, _ := reader.Peek(len(finalExecPlanPrefixV2))
	if string(peek) != finalExecPlanPrefixV2 {
		// Preserve v1's exact EOF/trailing-data codec, including a valid JSON
		// object without a newline. Peek does not consume its ordinary bytes.
		if err := decodeHelper(reader, &plan); err != nil {
			return plan, nil, err
		}
		if plan.Version != 1 {
			return plan, nil, fmt.Errorf("unknown sealed VMM execution version")
		}
		return plan, nil, nil
	}
	if _, err := reader.Discard(len(finalExecPlanPrefixV2)); err != nil {
		return plan, nil, err
	}
	data, err := proto.ReadJSONLine(reader)
	if err != nil {
		return plan, nil, err
	}
	if len(data) > helperLimit-len(finalExecPlanPrefixV2)-2 {
		return plan, nil, fmt.Errorf("direct VMM final plan exceeds its original bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return plan, nil, fmt.Errorf("direct VMM final plan has trailing metadata")
	}
	if plan.Version != 2 {
		return plan, nil, fmt.Errorf("direct VMM final prefix has another plan version")
	}
	return plan, reader, nil
}

func waitFinalExecContinueV2(reader *bufio.Reader) error {
	if reader == nil {
		return fmt.Errorf("direct VMM final continuation reader is missing")
	}
	value, err := reader.ReadByte()
	if err != nil {
		return fmt.Errorf("direct VMM final continuation was not issued: %w", err)
	}
	if value != finalExecContinueV2 {
		return fmt.Errorf("unknown direct VMM final continuation")
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("direct VMM final continuation has surplus data")
	}
	return nil
}

// This is the original runner's one startup continuation, before its normal
// API decoder begins. Byte-at-a-time native reads preserve that later stream.
type modeContinueReaderV1 struct {
	conn *net.UnixConn
	peer *unix.Ucred
}

func (reader modeContinueReaderV1) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	n, control, err := proto.ReceiveUnixControlV1(reader.conn, data[:1], 0)
	defer control.Close()
	if err != nil {
		return n, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	if err := control.CheckReceivedV1(reader.conn); err != nil {
		return 0, err
	}
	c := control.Credentials
	if c == nil || reader.peer == nil || c.PID != int(reader.peer.Pid) || c.UID != reader.peer.Uid || c.GID != reader.peer.Gid {
		return 0, fmt.Errorf("mode continuation has another native original controller")
	}
	return n, nil
}

func (server *runnerServer) receiveModeContinueV1(ctx context.Context, until time.Time, writer io.WriteCloser) error {
	conn, ok := server.conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("mode continuation lost its original native channel")
	}
	if err := proto.EnableUnixCredentialsV1(conn); err != nil {
		return err
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var peer *unix.Ucred
	var observed error
	err = raw.Control(func(fd uintptr) { peer, observed = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || observed != nil || peer == nil || peer.Pid <= 0 {
		return errors.Join(err, observed, fmt.Errorf("mode controller has no original native peer"))
	}
	if err := conn.SetReadDeadline(until); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		conn.SetReadDeadline(time.Time{})
	}()
	data, err := proto.ReadJSONLine(bufio.NewReader(modeContinueReaderV1{conn: conn, peer: peer}))
	if err != nil {
		return err
	}
	var request runnerRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("mode continuation has trailing metadata")
	}
	if request.ID != 0 || request.Op != "mode_continue_v1" {
		return fmt.Errorf("mode continuation has another operation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n, err := writer.Write([]byte{finalExecContinueV2}); err != nil {
		return err
	} else if n != 1 {
		return io.ErrShortWrite
	}
	return writer.Close()
}
