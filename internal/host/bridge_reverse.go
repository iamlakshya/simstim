package host

import (
	"bufio"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (b *bridge) reverse(reader *bufio.Reader, request proto.Request) error {
	if err := CheckCaller(); err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	var args api.PortArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.ID) || args.Port < 1 || args.Port > 65535 {
		err := UserError("bad_port", "invalid reverse forward request", "Name a sandbox and one exact guest port from 1 to 65535.")
		b.reply(request.ID, nil, err)
		return err
	}
	manifest, err := LoadManifest(args.ID)
	if err != nil || manifest.ID != args.ID || manifest.Wall == nil {
		err = UserError("not_walled", "reverse forwarding needs a running sandbox", "Start or resume a sandbox before forwarding a laptop service.")
		b.reply(request.ID, nil, err)
		return err
	}
	line, hello, err := dialSessionHolder(args.ID, 10*time.Second)
	if err != nil {
		b.reply(request.ID, nil, UserError("sandbox_stopped", "the sandbox is unavailable for a reverse forward", "Resume it, then forward the laptop service again."))
		return err
	}
	defer line.c.Close()
	if err := requireSupervisorFeatures(hello, api.FeatureReversePortsV1); err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	if err := line.w.WriteJSON(proto.TRequest, request); err != nil {
		b.reply(request.ID, nil, UserError("port_refused", "the supervisor did not take the reverse request", "Check the sandbox and forward the service again."))
		return err
	}
	frame, err := proto.Read(line.r)
	var response proto.Response
	if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != request.ID {
		err = UserError("protocol", "the supervisor sent an invalid reverse response", "Upgrade the helper and resume the sandbox.")
		b.reply(request.ID, nil, err)
		return err
	}
	if err := b.w.Write(proto.TResponse, frame.Payload); err != nil {
		return err
	}
	if !response.OK {
		return nil
	}
	_ = line.c.SetDeadline(time.Time{})
	done := make(chan error, 1)
	go func() { done <- proto.RelayStreams(line.r, b.w); _ = line.c.Close() }()
	err = proto.RelayStreams(reader, line.w)
	_ = line.c.Close()
	<-done
	return err
}
