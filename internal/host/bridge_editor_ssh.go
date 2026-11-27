package host

import (
	"bufio"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() {
	Features = append(Features, proto.FeatureEditorSSHV1)
	sessionOps[proto.EditorSSHOpenV1] = true
	sessionOps["ssh.view"] = true
}

// The bridge transfers exactly this dedicated accepted connection to the
// selected original supervisor. No token registry joins unrelated peers.
func (b *bridge) editorSSHV1(reader *bufio.Reader, request proto.Request) error {
	var args proto.EditorSSHOpenArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) {
		err := UserError("bad_request", "invalid SSH session selection", "Use the exact generated session SSH alias.")
		b.reply(request.ID, nil, err)
		return err
	}
	manifest, err := LoadManifest(args.Session)
	if err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	if manifest.Wall == nil || manifest.Life != nil && manifest.Life.Phase == LifeStopped {
		err = UserError("sandbox_stopped", "the selected SSH sandbox is not running", "Resume its session, then reconnect with SSH.")
		b.reply(request.ID, nil, err)
		return err
	}
	line, hello, err := dialSessionHolder(args.Session, 10*time.Second)
	if err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	defer line.c.Close()
	if err = requireSupervisorFeatures(hello, proto.FeatureEditorSSHV1); err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	if err = line.w.WriteJSON(proto.TRequest, request); err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	frame, err := proto.Read(line.r)
	if err != nil {
		b.reply(request.ID, nil, err)
		return err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != request.ID {
		err = UserError("protocol", "the supervisor sent an invalid SSH answer", "Update the client and host helper.")
		b.reply(request.ID, nil, err)
		return err
	}
	if err = b.w.Write(proto.TResponse, frame.Payload); err != nil {
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
