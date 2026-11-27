//go:build linux

package guest

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/iamlakshya/simstim/internal/api"
	"io"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type editorPTYOutputV1 struct{ wire *link.EditorTerminalV1 }

func (w editorPTYOutputV1) Write(data []byte) (int, error) {
	return w.wire.WriteData(proto.TOutput, data)
}

// This is an init-owned guest command, without a holder, saved manifest, row,
// launch event or restore intent. Its original registered run owns every PID.
func (g *Init) serveEditorTerminalV1(st *link.Stream) {
	run, _, err := g.takeEditorV1(st, proto.EditorTerminalV1)
	if err != nil {
		_ = st.Close()
		return
	}
	defer func() { _ = st.Close(); g.finishCommand(run) }()
	frame, err := link.ReadEditorFrameV1(st, 8<<10)
	var header proto.EditorTerminalConfigV1
	if err != nil || frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, header) || json.Unmarshal(frame.Payload, &header) != nil {
		return
	}
	if header.Term == "" || len(header.Term) > 128 || !utf8.ValidString(header.Term) || strings.ContainsAny(header.Term, "\x00\r\n\x1b") || header.Cols == 0 || header.Rows == 0 || header.Cols > 1000 || header.Rows > 1000 || len(header.Modes) > 4096 {
		return
	}
	env := run.env[:0]
	for _, value := range run.env {
		if !strings.HasPrefix(value, "TERM=") {
			env = append(env, value)
		}
	}
	run.env = append(env, "TERM="+header.Term)
	cmd, _, err := g.agentCommand(run, nil, nil)
	if err != nil {
		return
	}
	master, slave, err := pty.Open()
	if err != nil {
		return
	}
	defer master.Close()
	defer slave.Close()
	if err = applyEditorModesV1(int(slave.Fd()), header.Modes); err != nil {
		return
	}
	if err = pty.Setsize(master, &pty.Winsize{Cols: uint16(header.Cols), Rows: uint16(header.Rows)}); err != nil {
		return
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
	run.abortOutput = func() { _ = st.SetWriteDeadline(time.Now()) }
	if err = g.startOwned(run, cmd); err != nil {
		return
	}
	_ = slave.Close()
	// A positive SSH startup answer follows this actual child Start.
	if err = proto.NewWriter(st).WriteJSON(proto.TResponse, proto.EditorReadyV1{Version: 1, Ready: true}); err != nil {
		run.cancel()
		_ = run.signal(uint32(syscall.SIGKILL))
		_, _ = g.waitOwned(run)
		return
	}
	wire := link.NewEditorTerminalV1(st)
	inputDone, outputDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(inputDone)
		defer run.cancel()
		inputEOF := false
		for {
			frame, err := wire.Read()
			if err != nil {
				return
			}
			switch frame.Type {
			case proto.TInput:
				if inputEOF {
					return
				}
				if _, err = master.Write(frame.Payload); err != nil {
					return
				}
			case proto.TResize:
				var size proto.Resize
				if link.DecodeEditorControlV1(frame.Payload, &size) != nil || size.Cols < 1 || size.Rows < 1 || size.Cols > 1000 || size.Rows > 1000 {
					return
				}
				if pty.Setsize(master, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)}) != nil {
					return
				}
			case proto.TRequest:
				var signal proto.EditorTerminalControlV1
				if link.DecodeEditorControlV1(frame.Payload, &signal) != nil {
					return
				}
				if signal.InputEOF {
					if inputEOF || signal.Signal != 0 {
						return
					}
					inputEOF = true
					settings, err := unix.IoctlGetTermios(int(master.Fd()), unix.TCGETS)
					if err != nil {
						return
					}
					if settings.Lflag&unix.ICANON != 0 {
						if _, err = master.Write([]byte{settings.Cc[unix.VEOF], settings.Cc[unix.VEOF]}); err != nil {
							return
						}
					}
					continue
				}
				if !editorSignalAllowedV1(signal.Signal) {
					return
				}
				if run.signal(signal.Signal) != nil {
					return
				}
			default:
				return
			}
		}
	}()
	var outputErr error
	go func() { defer close(outputDone); _, outputErr = io.Copy(editorPTYOutputV1{wire}, master) }()
	stopped := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-run.ctx.Done():
			_ = run.signal(uint32(syscall.SIGKILL))
			_ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait))
		case <-stopped:
		}
	}()
	known, waitErr := g.waitOwned(run)
	close(stopped)
	<-watchDone
	_ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait))
	select {
	case <-outputDone:
	case <-time.After(ctlCloseWait):
		_ = master.Close()
		<-outputDone
	}
	code := execWaitCode(waitErr)
	if !known || outputErr != nil && !errors.Is(outputErr, syscall.EIO) && !errors.Is(outputErr, io.EOF) {
		code = 125
	}
	_ = wire.Control(proto.TEnded, proto.Ended{ExitCode: code})
	_ = st.CloseWrite()
	_ = master.Close()
	_ = st.SetReadDeadline(time.Now())
	<-inputDone
}

func editorSignalAllowedV1(number uint32) bool {
	switch syscall.Signal(number) {
	case syscall.SIGABRT, syscall.SIGALRM, syscall.SIGFPE, syscall.SIGHUP, syscall.SIGILL, syscall.SIGINT, syscall.SIGKILL, syscall.SIGPIPE, syscall.SIGQUIT, syscall.SIGSEGV, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2:
		return true
	}
	return false
}

// RFC 4254 terminal modes are applied to this new slave before Start. Unknown
// opcodes refuse the terminal; none become an environment variable or ioctl.
func applyEditorModesV1(fd int, modes []byte) error {
	if len(modes) == 0 || len(modes) > 4096 {
		return execError("invalid terminal modes")
	}
	settings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	cc := map[byte]int{1: unix.VINTR, 2: unix.VQUIT, 3: unix.VERASE, 4: unix.VKILL, 5: unix.VEOF, 6: unix.VEOL, 7: unix.VEOL2, 8: unix.VSTART, 9: unix.VSTOP, 10: unix.VSUSP, 12: unix.VREPRINT, 13: unix.VWERASE, 14: unix.VLNEXT, 18: unix.VDISCARD}
	input := map[byte]uint32{30: unix.IGNPAR, 31: unix.PARMRK, 32: unix.INPCK, 33: unix.ISTRIP, 34: unix.INLCR, 35: unix.IGNCR, 36: unix.ICRNL, 37: unix.IUCLC, 38: unix.IXON, 39: unix.IXANY, 40: unix.IXOFF, 41: unix.IMAXBEL, 42: unix.IUTF8}
	local := map[byte]uint32{50: unix.ISIG, 51: unix.ICANON, 52: unix.XCASE, 53: unix.ECHO, 54: unix.ECHOE, 55: unix.ECHOK, 56: unix.ECHONL, 57: unix.NOFLSH, 58: unix.TOSTOP, 59: unix.IEXTEN, 60: unix.ECHOCTL, 61: unix.ECHOKE, 62: unix.PENDIN}
	output := map[byte]uint32{70: unix.OPOST, 71: unix.OLCUC, 72: unix.ONLCR, 73: unix.OCRNL, 74: unix.ONOCR, 75: unix.ONLRET}
	control := map[byte]uint32{92: unix.PARENB, 93: unix.PARODD}
	set := func(flags *uint32, mask, value uint32) {
		if value != 0 {
			*flags |= mask
		} else {
			*flags &^= mask
		}
	}
	for at := 0; at < len(modes); {
		op := modes[at]
		at++
		if op == 0 {
			if at != len(modes) {
				return execError("trailing terminal mode data")
			}
			return unix.IoctlSetTermios(fd, unix.TCSETS, settings)
		}
		if at+4 > len(modes) {
			return execError("truncated terminal mode")
		}
		value := binary.BigEndian.Uint32(modes[at : at+4])
		at += 4
		if index, ok := cc[op]; ok {
			if value > 255 {
				return execError("terminal control character is too large")
			}
			settings.Cc[index] = uint8(value)
			continue
		}
		if flag, ok := input[op]; ok {
			set(&settings.Iflag, flag, value)
			continue
		}
		if flag, ok := local[op]; ok {
			set(&settings.Lflag, flag, value)
			continue
		}
		if flag, ok := output[op]; ok {
			set(&settings.Oflag, flag, value)
			continue
		}
		if flag, ok := control[op]; ok {
			set(&settings.Cflag, flag, value)
			continue
		}
		switch op {
		case 11, 15, 16, 17: // BSD-only controls have no Linux slave equivalent.
		case 90:
			if value != 0 {
				settings.Cflag = settings.Cflag&^unix.CSIZE | unix.CS7
			}
		case 91:
			if value != 0 {
				settings.Cflag = settings.Cflag&^unix.CSIZE | unix.CS8
			}
		case 128:
			settings.Ispeed = value
		case 129:
			settings.Ospeed = value
		default:
			return execError("unsupported terminal mode")
		}
	}
	return execError("terminal modes have no terminator")
}
