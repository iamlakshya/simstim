package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A holder serves two sockets (spec 10.4):
//
//   - The session socket, paths.Socket, with the whole holder protocol: the
//     bridge, the pick menu and the user's own programs attach, type, stop
//     and ask there. With a systemd user manager, each session runs in a unit
//     of its own under the sessions' slice. A peer in any unit or scope of
//     that slice, or one whose cgroup the holder cannot read, then gets only
//     hello and ping, and forbidden for the rest. It takes no hook events.
//   - The hook socket, paths.HookSocket, which the agent's environment names.
//     It accepts only the role hook and the op hook. With the peer check, a
//     peer in another session's unit or a git scope, or one whose cgroup the
//     holder cannot read, gets forbidden there too.
//
// Without a systemd user manager, sessions have no unit, and the holder
// cannot tell a session's program from another of the user's. It keeps the
// role check alone, and the doctor reports the session sockets as weak.
//
// The check holds a session's own processes only. A program that leaves the
// sessions' slice, with systemd-run --user or by writing its pid to a
// cgroup.procs file that the user owns, passes it. Only a wall closes that.

// roleHook is the role that an agent's hook gives in its hello.
const roleHook = "hook"

// access is what one connection to a holder may do. Its zero value allows
// nothing: only accessOf gives more.
type access uint8

const (
	// accessNone allows nothing.
	accessNone access = iota
	// accessFull allows every frame and request but hook.
	accessFull
	// accessPing allows hello and ping only: a peer inside a session.
	accessPing
	// accessHook allows the op hook only: the hook socket.
	accessHook
)

// The refusals, each with the error code forbidden.
var (
	errInside = &proto.Error{Code: "forbidden",
		Message: "a program inside a session cannot attach to, type into or stop a session",
		Hint:    "Run it from outside every session, such as from the client on your laptop."}
	errHookOnly = &proto.Error{Code: "forbidden",
		Message: "the hook socket accepts only hook events",
		Hint:    "Send other requests to the session's own socket, from outside every session."}
	errOtherHook = &proto.Error{Code: "forbidden",
		Message: "a hook can reach only its own session",
		Hint:    "Run the hook from the session's own agent."}
	errNoHooksHere = &proto.Error{Code: "forbidden",
		Message: "the session socket takes no hook events",
		Hint:    "Send hook events to the hook socket that the agent's environment names."}
)

// fullPeer reports whether the peer of conn, on the session socket, may do
// everything: always without the peer check, and otherwise when it runs
// outside every session.
func (h *Holder) fullPeer(conn net.Conn) bool {
	return !h.peerCheck || h.placeOf(conn) == placeOutside
}

// accessOf decides what a connection may do, from the socket it came to and
// the role in its hello. A refusal comes back as an error to send before the
// connection closes.
func (h *Holder) accessOf(conn net.Conn, hookSock bool, role string) (access, *proto.Error) {
	if !hookSock {
		if h.fullPeer(conn) {
			return accessFull, nil
		}
		return accessPing, nil
	}
	if role != roleHook {
		return accessNone, errHookOnly
	}
	// The session's own unit (placeOwn) and a program outside every session
	// (placeOutside) may send hook events. A program outside could do more
	// on the session socket. Another session's unit, a git scope or a peer
	// that cannot be read (placeInside) may not.
	if h.peerCheck && h.placeOf(conn) == placeInside {
		return accessNone, errOtherHook
	}
	return accessHook, nil
}

// serveLimited answers one frame from a connection without full access. A
// ping gets its pong, and on the hook socket the op hook is served. Any
// other frame gets forbidden, and the connection then closes: it returns
// false.
func (h *Holder) serveLimited(c *hclient, f proto.Frame) bool {
	refusal := errInside
	if c.access == accessHook {
		refusal = errHookOnly
	}
	switch f.Type {
	case proto.TPing:
		if c.access == accessPing {
			_ = c.w.Write(proto.TPong, f.Payload)
			return true
		}
	case proto.TRequest:
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) != nil {
			return false
		}
		if req.Op == "hook" && c.access == accessHook {
			h.request(c, req)
			return true
		}
		h.log.Warn("refused a request", "op", req.Op, "hook_socket", c.access == accessHook)
		_ = c.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: refusal})
		return false
	default:
		// Every other frame is refused below.
	}
	h.log.Warn("refused a frame", "type", int(f.Type), "hook_socket", c.access == accessHook)
	_ = c.w.WriteJSON(proto.TError, refusal)
	return false
}

// listen opens the session socket and the hook socket, each 0600 in a 0700
// folder, and turns the peer check on when the holder runs in the unit that
// launchHolder started for it.
func (h *Holder) listen() error {
	sock, hook := paths.Socket(h.id), paths.HookSocket(h.id)
	for _, dir := range []string{filepath.Dir(filepath.Dir(sock)), filepath.Dir(sock), filepath.Dir(hook)} {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return err
		}
	}
	ln, err := listenPrivate(sock, "the session's socket")
	if err != nil {
		return err
	}
	hookLn, err := listenPrivate(hook, "the session's hook socket")
	if err != nil {
		ln.Close()
		return err
	}
	h.ln, h.hookLn = ln, hookLn
	h.peerCheck = inOwnUnit(h.id)
	h.log.Info("sockets open", "peer_check", h.peerCheck)
	return nil
}

// listenPrivate listens on the Unix socket sock, mode 0600, in place of any
// file there. what names the socket in errors.
func listenPrivate(sock, what string) (net.Listener, error) {
	_ = os.Remove(sock)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", what, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("make %s private: %w", what, err)
	}
	return ln, nil
}

// closeSockets closes both listeners and removes their files.
func (h *Holder) closeSockets() {
	for _, ln := range []net.Listener{h.ln, h.hookLn} {
		if ln != nil {
			ln.Close()
		}
	}
	os.Remove(paths.Socket(h.id))
	os.Remove(paths.HookSocket(h.id))
}
