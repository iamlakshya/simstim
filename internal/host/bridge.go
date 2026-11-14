package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Features this host helper offers. Clients check them before using a feature.
var Features = []string{"attach.replay", "upload", "peek", "setup", "rename", "send", "hooks", "restore", "ports", "worktree", "folders", "alerts", "trace", api.FeatureSandboxReviewContext, api.FeatureSandboxCohort, inputtransfer.Feature, inputtransfer.FinishFeature, api.FeatureExecStream, api.FeatureSpawn, api.FeatureSandboxGroupRM, api.FeatureSandboxSnapshot, "session.id", agentbridge.Feature}

type bridge struct {
	copyMu                             sync.Mutex
	copyBusy, copyClosed, copyWatching bool
	copyRoute                          *bridgeCopyRoute
	pullClosed                         bool
	pull                               *bridgePull
	ownedOutput                        OwnedBridgeOutput
	agents                             agentsDiskService
	images                             imageService
	runtime                            runtimeService
	ctx                                context.Context
	w                                  *proto.Writer
	// log takes the bridge's debug lines, while its debug is on
	// (bridge_debug.go).
	log *slog.Logger
	dbg bridgeDebug
	bun bundleFiles // what debug.collect listed (bridge_collect.go)

	// traced is true while the attach asked for a debug trace of its keys.
	// Only the loop in RunBridge uses it.
	traced bool

	attachMu sync.Mutex
	ended    bool

	mu     sync.Mutex
	holder net.Conn
	hw     *proto.Writer

	// umu guards uploads, the uploads that have begun and not ended, the
	// files of each, and uploadsDone, whether the bridge takes no more
	// (bridge_upload.go). uploadsWG counts every tracked upload until its
	// disk cleanup or finalisation has finished.
	umu              sync.Mutex
	uploads          map[uint64]*upload
	finishingUploads map[*upload]struct{}
	uploadsDone      bool
	uploadsWG        sync.WaitGroup
}

// RunBridge serves one client over in and out (the SSH channel's stdio).
func RunBridge(in io.Reader, out io.Writer) error {
	return runBridge(in, out, nil)
}

func runBridge(in io.Reader, out io.Writer, owned OwnedBridgeOutput) (returned error) {
	if err := secretmem.Protect(); err != nil {
		return err
	}
	r := bufio.NewReaderSize(in, 256<<10)
	w := proto.NewWriter(out)
	f, err := proto.Read(r)
	if err != nil {
		return err
	}
	if f.Type != proto.THello {
		return errors.New("expected hello")
	}
	var hello proto.Hello
	if err := json.Unmarshal(f.Payload, &hello); err != nil {
		return fmt.Errorf("read the client's hello: %w", err)
	}
	if err := hello.Check(version.Protocol); err != nil {
		_ = w.WriteJSON(proto.TError, proto.Error{Code: "protocol", Message: err.Error(), Hint: "Update the client."})
		return err
	}
	hostID, err := HostID()
	if err != nil {
		_ = w.WriteJSON(proto.TError, proto.Error{Code: "host_id", Message: err.Error(), Hint: "Check " + paths.HostID() + " on the host."})
		return err
	}
	features := Features
	if owned == nil {
		features = slices.DeleteFunc(slices.Clone(Features), func(feature string) bool { return feature == api.FeaturePullBundle || feature == api.FeaturePullRepositories })
	}
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge", HostID: hostID, Features: features}); err != nil {
		return err
	}
	lines := diag.NewLines()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &bridge{ctx: ctx, ownedOutput: owned, w: w, log: slog.New(lines), dbg: bridgeDebug{lines: lines}, uploads: map[uint64]*upload{}}
	liveBridges.Store(b, true)
	defer liveBridges.Delete(b)
	// An upload that never got its end, as one that its client gave up at
	// a cancel, leaves nothing when the connection goes.
	defer b.reclaimUploadsAt(true)
	defer b.stopDebug("end")
	defer b.endAttach()
	defer func() {
		cancel()
		returned = errors.Join(returned, b.agents.close(), b.images.close())
		b.runtime.close()
		if owned != nil {
			_ = owned.Cancel()
		}
		b.closePull()
		b.closeCopy()
	}()
	inside := CheckCaller() != nil // bridge_inside.go
	firstFrame := true
	for {
		f, err := proto.Read(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		wasFirst := firstFrame
		firstFrame = false
		if inside && b.refuseInside(f) {
			continue
		}
		switch f.Type {
		case proto.TPing:
			_ = w.Write(proto.TPong, proto.Pong(f.Payload, diag.Now()))
		case proto.TRequest:
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) == nil {
				if req.Op == "inputs.begin" || req.Op == inputtransfer.Describe {
					if !wasFirst {
						err := UserError("bad_request", "inputs require a dedicated connection", "Open a fresh input continuation.")
						b.reply(req.ID, nil, err)
						return err
					}
					return b.inputs(r, req)
				}
				if req.Op == "exec" {
					if !wasFirst {
						err := UserError("bad_request", "exec requires a dedicated connection", "Open a fresh connection for exec.")
						b.reply(req.ID, nil, err)
						return err
					}
					return b.exec(r, req)
				}
				if IsPullOp(req.Op) {
					run, ctx, err := b.preparePull(req)
					if err != nil {
						b.replyPull(req.ID, nil, err)
						continue
					}
					if run == nil {
						continue
					}
					liveRequests.Add(1)
					go func() { defer liveRequests.Add(-1); result, err := run(ctx); b.replyPull(req.ID, result, err) }()
					continue
				}
				liveRequests.Add(1) // EndBridge waits for it
				go func() {
					defer liveRequests.Add(-1)
					b.serve(req)
				}()
			}
		case proto.TAttach:
			var a proto.Attach
			if json.Unmarshal(f.Payload, &a) == nil {
				b.note("attach", 0)
				if err := b.attach(a); err != nil {
					_ = w.WriteJSON(proto.TError, proto.Error{Code: "attach_failed", Message: err.Error(), Hint: "List sessions; it may have been deleted."})
				}
			}
		case proto.TInput:
			if b.traced {
				// When the bridge read the key, for the holder's trace.
				b.forward(proto.Frame{Type: proto.TTrace, Payload: proto.EncodeTime(diag.Now())})
			}
			b.forward(f)
		case proto.TResize:
			b.forward(f)
		case proto.TDetach:
			b.note("detach", 0)
			b.forward(f)
			b.closeHolder()
		case proto.TUploadBegin:
			b.uploadBegin(f.Payload)
		case proto.TUploadChunk:
			b.uploadChunk(f.Payload)
		case proto.TUploadEnd:
			b.uploadEnd(f.Payload)
		default:
			// A client sends no other frame: drop it.
		}
	}
}

func (b *bridge) handle(req proto.Request) {
	switch req.Op {
	case api.SandboxReviewContextOp:
		var args api.SandboxSnapshotArgs
		fields, err := api.JSONObjectFields(req.Args, "id")
		if err != nil || len(fields) != 1 || !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.Cohort {
			b.reply(req.ID, nil, UserError("bad_request", "invalid saved sandbox review request", "Use the original local publication receipt with the current helper."))
			return
		}
		view, err := SavedSandboxReviewContext(b.ctx, args.ID)
		b.reply(req.ID, view, err)
	case "sandbox.snapshot":
		var a api.SandboxSnapshotArgs
		if err := json.Unmarshal(req.Args, &a); err != nil || !api.ExactJSONFields(req.Args, api.SandboxSnapshotArgs{}) {
			b.reply(req.ID, nil, UserError("bad_request", "invalid sandbox snapshot request", "Use the current client and helper."))
			return
		}
		if a.Cohort {
			view, err := SandboxCohortSnapshot(context.Background(), a.ID)
			b.reply(req.ID, view, err)
			return
		}
		id, err := SandboxPrimaryID(a.ID)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		if alive(id) {
			if err := checkSessionFeatures(id, 3*time.Second, api.FeatureSpawn, api.FeatureSandboxSnapshot); err != nil {
				b.reply(req.ID, nil, err)
				return
			}
		}
		result, err := SandboxSnapshot(a.ID)
		b.reply(req.ID, result, err)
		return
	case "spawn":
		if err := validateJoinSecretShape(req.Args); err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		var a api.JoinArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		id, err := SandboxPrimaryID(a.Parent)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		required := []string{api.FeatureSpawn, api.FeatureSandboxSnapshot}
		if a.SecretDelivery != nil {
			required = append(required, api.FeatureBoundSecrets)
		}
		result, err := HolderRequestWithFeatures(id, "spawn", a, 30*time.Second, required...)
		b.reply(req.ID, json.RawMessage(result), err)
		return
	case "ls":
		v, err := List()
		b.reply(req.ID, v, err)
	case "new":
		a, err := decodeNewArgs(req.Args)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		v, err := New(a)
		b.reply(req.ID, v, err)
	case "delete":
		var a struct {
			ID string `json:"id"`
			// Name, when it is there, is the name that the client expects,
			// "" too: the host refuses the delete when the session has
			// another. Without it, the delete takes the session whatever
			// its name.
			Name    *string   `json:"name"`
			Members *[]string `json:"members,omitempty"`
		}
		_ = json.Unmarshal(req.Args, &a)
		var res DeleteResult
		var err error
		if a.Name != nil && a.Members != nil {
			res, err = DeleteNamedGroup(a.ID, *a.Name, *a.Members)
		} else if a.Name != nil {
			res, err = DeleteNamed(a.ID, *a.Name)
		} else {
			res, err = Delete(a.ID)
		}
		b.reply(req.ID, res, err)
	case "rename":
		var a struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, map[string]string{"name": a.Name}, Rename(a.ID, a.Name))
	case "peek":
		var a struct {
			ID    string `json:"id"`
			Bytes int    `json:"bytes"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, ""))
			return
		}
		res, err := HolderRequest(a.ID, "peek", map[string]int{"bytes": a.Bytes}, 3*time.Second)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		b.reply(req.ID, res, nil)
	case "send":
		var a struct {
			ID    string `json:"id"`
			Text  string `json:"text"`
			Enter bool   `json:"enter"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, ""))
			return
		}
		res, err := HolderRequest(a.ID, "send", map[string]any{"text": a.Text, "enter": a.Enter}, 3*time.Second)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		b.reply(req.ID, res, nil)
	case "ports":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, ""))
			return
		}
		res, err := HolderRequest(a.ID, "ports", nil, 3*time.Second)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		b.reply(req.ID, res, nil)
	case "folders":
		var a struct {
			Prefix string `json:"prefix"`
			Limit  int    `json:"limit"`
		}
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, Folders(a.Prefix, a.Limit), nil)
	case "restore":
		var a struct {
			All bool `json:"all"`
		}
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, Restore(a.All), nil)
	case "doctor":
		b.reply(req.ID, Doctor(), nil)
	case "setup":
		var a SetupArgs
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, Setup(a), nil)
	case "alerts.get", "alerts.set", "alerts.test", "alerts.log", "alerts.off":
		v, err := alertsRequest(req.Op, req.Args)
		b.reply(req.ID, v, err)
	default:
		if !b.handleCopy(req) && !b.handlePolicy(req) && !b.handleAgentsDisk(req) && !b.handleRuntime(req) && !b.handleImages(req) && !b.handlePull(req) && !b.handleMore(req) && !b.handleDebug(req) {
			b.reply(req.ID, nil, UserError("unknown_op", "unknown request "+req.Op, "Update the host helper."))
		}
	}
}

func safeName(n string) string {
	n = filepath.Base(n)
	var sb strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	s := strings.TrimLeft(sb.String(), ".")
	if s == "" {
		s = "file"
	}
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

func cleanOld(dir string, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > age {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
