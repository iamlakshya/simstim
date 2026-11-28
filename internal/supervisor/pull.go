package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

const (
	pullStreamTimeout = 3 * time.Minute
	maxBundleHdrBytes = 64 << 10
)

// PullBundle streams a git bundle for walled session m strictly from inside
// the session's wall over link.KindBundle (isolation spec 6.10, 7.1). If the
// supervisor is running, it asks the live supervisor over paths.Socket(m.ID).
// If the session is stopped, it boots a temporary bundle-only sandbox (no
// agent, no holder, no egress, no secrets, leaving the session stopped), pulls
// the bundle, and destroys the temporary sandbox.
func PullBundle(m *host.Manifest, req api.PullBundleArgs, out io.Writer) error {
	if m == nil || m.Wall == nil {
		return writePullBundleErr(out, host.UserError("bad_request", "session is not walled", "Update the host helper."))
	}
	if req.Base != "" && !isValidGitOID(req.Base) {
		return writePullBundleErr(out, host.UserError("bad_request", "invalid base commit ID", "Update the client."))
	}
	if dialed, err := pullFromRunningSupervisor(m.ID, req, out); dialed {
		return err
	}

	unlock, err := host.LockSessionWait(m.ID, 5*time.Second)
	if err != nil {
		return writePullBundleErr(out, err)
	}
	defer unlock()

	reloaded, err := host.LoadManifest(m.ID)
	if err != nil {
		return writePullBundleErr(out, host.UserError("not_found", "no session "+m.ID, "List sessions to find its ID."))
	}
	if dialed, err := pullFromRunningSupervisor(reloaded.ID, req, out); dialed {
		return err
	}
	return pullFromStoppedSandbox(reloaded, req, out)
}

func pullFromRunningSupervisor(id string, req api.PullBundleArgs, out io.Writer) (dialed bool, err error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, dialErr := d.DialContext(context.Background(), "unix", paths.Socket(id))
	if dialErr != nil {
		return false, nil
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	w := proto.NewWriter(conn)
	r := bufio.NewReaderSize(conn, 64<<10)
	if err := w.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "bridge",
	}); err != nil {
		return true, writePullBundleErr(out, host.UserError("session_gone", "could not reach the session supervisor", "Check the session status with ls."))
	}
	hf, err := proto.Read(r)
	if err != nil || hf.Type != proto.THello {
		return true, writePullBundleErr(out, host.UserError("session_gone", "the session supervisor did not answer", "Check the session status with ls."))
	}
	rawArgs, _ := json.Marshal(req)
	if err := w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "pull.bundle", Args: rawArgs}); err != nil {
		return true, writePullBundleErr(out, host.UserError("session_gone", "could not request bundle from supervisor", "Try again."))
	}
	rf, err := proto.Read(r)
	if err != nil || rf.Type != proto.TResponse {
		return true, writePullBundleErr(out, host.UserError("session_gone", "the session supervisor closed before replying", "Try again."))
	}
	var resp proto.Response
	if err := json.Unmarshal(rf.Payload, &resp); err != nil {
		return true, writePullBundleErr(out, host.UserError("protocol", "invalid pull reply from supervisor", "Update the host helper."))
	}
	if !resp.OK {
		pe := resp.Error
		if pe == nil {
			pe = &proto.Error{Code: "git", Message: "bundle pull failed", Hint: "Try again."}
		}
		return true, writePullBundleErr(out, pe)
	}
	_ = conn.SetDeadline(time.Now().Add(pullStreamTimeout))
	if _, err := io.Copy(out, r); err != nil {
		return true, fmt.Errorf("relay bundle from supervisor: %w", err)
	}
	return true, nil
}

// handlePullBundle serves a "pull.bundle" request on the live supervisor socket
// by opening link.KindBundle to guest init and streaming the validated header
// and capped bundle bytes back over conn.
func (s *Supervisor) handlePullBundle(conn net.Conn, w *proto.Writer, req proto.Request) bool {
	fail := func(code, msg, hint string) bool {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    req.ID,
			Error: &proto.Error{Code: code, Message: msg, Hint: hint},
		})
		return false
	}
	if s.link == nil {
		return fail("sandbox_stopped", "the sandbox link is not running", "Resume the session and try again.")
	}
	var args api.PullBundleArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return fail("bad_request", "the pull.bundle request is not valid JSON", "Update the client.")
		}
	}
	if args.Base != "" && !isValidGitOID(args.Base) {
		return fail("bad_request", "invalid base commit ID", "Update the client.")
	}
	s.mu.Lock()
	mCopy := *s.m
	wallLvl, wallDrv := "", ""
	if s.m.Wall != nil {
		wallLvl, wallDrv = s.m.Wall.Level, s.m.Wall.Driver
	}
	s.mu.Unlock()

	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true}); err != nil {
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), pullStreamTimeout)
	defer cancel()
	head, size, empty, _ := streamBundleFromLink(ctx, s.link, &mCopy, args, conn)

	detail := fmt.Sprintf("empty=%t size=%d", empty, size)
	if head != "" {
		detail += " head=" + head[:min(len(head), 12)]
	}
	s.audit.Record(AuditEntry{
		Event:   "pull",
		Session: s.id,
		Wall:    wallLvl,
		Driver:  wallDrv,
		Detail:  detail,
	})
	return true
}

// handlePulled records commit OIDs pulled to the user's laptop in the live
// supervisor's manifest and audit log (isolation spec 6.10).
func (s *Supervisor) handlePulled(w *proto.Writer, req proto.Request) {
	reply := func(v any, perr *proto.Error) {
		resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
		if v != nil {
			resp.Result, _ = json.Marshal(v)
		}
		_ = w.WriteJSON(proto.TResponse, resp)
	}
	var args struct {
		Commits []string `json:"commits"`
	}
	if err := json.Unmarshal(req.Args, &args); err != nil {
		reply(nil, &proto.Error{Code: "bad_request", Message: "the pulled request is not valid JSON", Hint: "Update the client."})
		return
	}
	var valid []string
	for _, c := range args.Commits {
		if !isValidGitOID(c) {
			reply(nil, &proto.Error{Code: "bad_request", Message: "invalid commit ID in pulled request", Hint: "Update the client."})
			return
		}
		valid = append(valid, c)
	}
	if len(valid) == 0 {
		reply(map[string]bool{"ok": true}, nil)
		return
	}
	s.mu.Lock()
	var added []string
	for _, c := range valid {
		if !slices.Contains(s.m.Pulled, c) {
			s.m.Pulled = append(s.m.Pulled, c)
			added = append(added, c)
		}
	}
	mCopy := *s.m
	s.mu.Unlock()
	if len(added) > 0 {
		_ = s.save(&mCopy, false)
		s.audit.Record(AuditEntry{
			Event:   "pulled",
			Session: s.id,
			Detail:  api.CleanText(strings.Join(added, ","), maxDetailRunes),
		})
	}
	reply(map[string]bool{"ok": true}, nil)
}

func streamBundleFromLink(ctx context.Context, sess *link.Session, m *host.Manifest, req api.PullBundleArgs, out io.Writer) (head string, size int64, empty bool, err error) {
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = host.DefaultPullMaxBytes
	}
	workDir, seedHead, err := host.ResolvePullGuestRepo(m, req.Repo)
	if err != nil {
		return "", 0, false, writePullBundleErr(out, err)
	}
	base := trustedPullBase(seedHead, req)
	st, err := sess.Open(ctx, link.KindBundle, guest.BundleHeader{
		Dir:      "out",
		Session:  m.ID,
		WorkDir:  workDir,
		Base:     base,
		Full:     req.Full,
		MaxBytes: maxBytes,
	})
	if err != nil {
		return "", 0, false, writePullBundleErr(out, host.UserError("session_gone", "could not open guest bundle stream: "+err.Error(), "Check the session status with ls."))
	}
	defer st.Close()

	br := bufio.NewReaderSize(st, 64<<10)
	line, err := readBoundedLine(br, maxBundleHdrBytes)
	if err != nil {
		return "", 0, false, writePullBundleErr(out, host.UserError("protocol", "guest closed before sending bundle header", "Try pulling again."))
	}
	var hdr api.PullBundleHeader
	if err := json.Unmarshal(line, &hdr); err != nil {
		return "", 0, false, writePullBundleErr(out, host.UserError("protocol", "guest sent an invalid bundle header", "Update the host helper."))
	}
	if hdr.Error != nil {
		cleanErr := cleanGuestError(&proto.Error{
			Code:    hdr.Error.Code,
			Message: hdr.Error.Message,
			Hint:    hdr.Error.Hint,
		}, "git", "bundle pull failed inside the sandbox", "Check the repository in the session.")
		return "", 0, false, writePullBundleErr(out, cleanErr)
	}
	cleanHdr, perr := sanitizeGuestBundleHeader(hdr, maxBytes)
	if perr != nil {
		return "", 0, false, writePullBundleErr(out, perr)
	}
	b, err := json.Marshal(cleanHdr)
	if err != nil {
		return "", 0, false, fmt.Errorf("encode bundle header: %w", err)
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		return "", 0, false, fmt.Errorf("write bundle header: %w", err)
	}
	if cleanHdr.Empty {
		return cleanHdr.Head, 0, true, nil
	}
	n, err := io.Copy(out, io.LimitReader(br, cleanHdr.Size))
	if err != nil {
		return cleanHdr.Head, n, false, fmt.Errorf("stream bundle bytes: %w", err)
	}
	if n != cleanHdr.Size {
		return cleanHdr.Head, n, false, fmt.Errorf("truncated guest bundle: got %d of %d bytes", n, cleanHdr.Size)
	}
	return cleanHdr.Head, n, false, nil
}

func trustedPullBase(seedHead string, req api.PullBundleArgs) string {
	if req.Full {
		return ""
	}
	if req.Base != "" && isValidGitOID(req.Base) {
		return req.Base
	}
	if isValidGitOID(seedHead) {
		return seedHead
	}
	return ""
}

func sanitizeGuestBundleHeader(hdr api.PullBundleHeader, maxBytes int64) (api.PullBundleHeader, *proto.Error) {
	if hdr.Repo == nil {
		return api.PullBundleHeader{}, &proto.Error{
			Code:    "protocol",
			Message: "guest bundle reply omitted repository info",
			Hint:    "Check the repository in the session.",
		}
	}
	if hdr.Head != "" && !isValidGitOID(hdr.Head) {
		return api.PullBundleHeader{}, &proto.Error{
			Code:    "protocol",
			Message: "guest sent an invalid commit ID",
			Hint:    "Check the repository in the session.",
		}
	}
	dirty := hdr.Repo.Dirty
	if dirty < 0 {
		dirty = 0
	}
	out := api.PullBundleHeader{
		Repo: &api.RepoInfo{
			Root:   api.CleanText(hdr.Repo.Root, 512),
			Main:   api.CleanText(hdr.Repo.Main, 512),
			Branch: api.CleanText(hdr.Repo.Branch, 256),
			Dirty:  dirty,
		},
		Head:  hdr.Head,
		Full:  hdr.Full,
		Empty: hdr.Empty,
	}
	if hdr.Empty {
		return out, nil
	}
	if hdr.Size <= 0 {
		return api.PullBundleHeader{}, &proto.Error{
			Code:    "protocol",
			Message: "guest sent an empty bundle",
			Hint:    "Try pulling again.",
		}
	}
	if hdr.Size > maxBytes {
		return api.PullBundleHeader{}, &proto.Error{
			Code:    "pull_too_big",
			Message: fmt.Sprintf("the git bundle is %d bytes, above the %d-byte limit", hdr.Size, maxBytes),
			Hint:    "Push the branch to a shared remote, or raise sandbox.pull_max in config.toml.",
		}
	}
	out.Size = hdr.Size
	return out, nil
}

func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("read bundle header line: %w", err)
		}
		if len(buf)+len(chunk) > limit {
			return nil, errors.New("header line too long")
		}
		buf = append(buf, chunk...)
		if !isPrefix {
			return buf, nil
		}
	}
}

func writePullBundleErr(out io.Writer, err error) error {
	pe := &proto.Error{Code: "error", Message: err.Error(), Hint: "Try pulling again."}
	var existing *proto.Error
	if errors.As(err, &existing) && existing != nil {
		pe = existing
	}
	hdr := api.PullBundleHeader{
		Error: &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint,omitempty"`
		}{Code: pe.Code, Message: pe.Message, Hint: pe.Hint},
	}
	b, _ := json.Marshal(hdr)
	_, _ = out.Write(append(b, '\n'))
	return nil
}

func isValidGitOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
