//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/version"
)

var ordinaryGitSocketEnvV2 = brand.Env("ORDINARY_GIT_SOCKET_V2")

const ordinaryGitWorkerRoleV2 = "ordinary-git-worker-v2"
const ordinaryGitParentRoleV2 = "ordinary-git-parent-v2"

type ordinaryGitWorkerPlanV2 struct {
	Version int    `json:"v"`
	Nonce   string `json:"nonce"`
	Branch  string `json:"branch"`
}

// The environment selects a bootstrap locator only. Native current sender,
// helper code/namespaces and the actual original parent are checked before
// any fixed read. No JSON, descriptor number or environment value is a Source.
func runOrdinaryGitWorkerEntryV2(args []string) (handled bool, returned error) {
	return runOrdinaryGitWorkerEntryV3(args, nil)
}

func runOrdinaryGitWorkerEntryV3(args []string, image *ordinaryPurgeGitImageV3) (handled bool, returned error) {
	path := os.Getenv(ordinaryGitSocketEnvV2)
	if path == "" {
		return false, nil
	}
	_ = os.Unsetenv(ordinaryGitSocketEnvV2)
	if len(args) != 1 || args[0] != gitOpPurgeInspectV1 {
		return true, sessionLeaseError("the native ordinary Git worker has another closed command")
	}
	if err := CheckCaller(); err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return true, err
	}
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		return true, errors.Join(err, connection.Close())
	}
	defer func() { returned = errors.Join(returned, line.Close()) }()
	deadline, _ := ctx.Deadline()
	_ = line.SetDeadline(deadline)
	hello := proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: ordinaryGitWorkerRoleV2}
	if err := writeOrdinaryGitWorkerHelloV3(ctx, line, hello, image); err != nil {
		return true, err
	}
	frame, err := line.ReadFrame()
	if err != nil {
		return true, err
	}
	if err := acceptOrdinaryGitParentHelloV3(ctx, line, frame, image); err != nil {
		return true, err
	}
	if err := line.nativePeer.Check(); err != nil {
		return true, err
	}
	peerContext, joinPeer, err := watchOrdinaryPurgePeerV2(line)
	if err != nil {
		return true, err
	}
	defer joinPeer()
	peerCancelDone := make(chan struct{})
	stopPeer := context.AfterFunc(peerContext, func() { defer close(peerCancelDone); cancel() })
	defer func() {
		if !stopPeer() {
			<-peerCancelDone
		}
	}()
	plan, err := readOrdinaryGitPlanV2(line)
	if err != nil {
		return true, err
	}
	roots := &ordinaryActiveGitRootsV2{}
	defer func() { returned = errors.Join(returned, roots.Close()) }()
	for _, target := range []**os.File{&roots.Work, &roots.Admin, &roots.Common} {
		actual, err := readOrdinaryGitPlanV2(line)
		if err != nil || actual != plan {
			return true, errors.Join(sessionLeaseError("the original Git worker changed its root-loan continuation"), err)
		}
		file, sender, err := line.takeLoanFile()
		if err != nil {
			return true, err
		}
		*target = file
		if err := line.nativePeer.CheckSender(sender); err != nil {
			return true, err
		}
	}
	if err := line.RejectAncillary(); err != nil {
		return true, err
	}
	_ = line.SetDeadline(time.Time{})
	// There are no more commands in this closed purpose. An EOF cancels the
	// existing original commands; it cannot dispatch a different operation.
	readerDone := make(chan struct{})
	var release proto.Frame
	var releaseErr error
	go func() {
		defer close(readerDone)
		release, releaseErr = line.ReadFrame()
		if releaseErr != nil {
			cancel()
		}
	}()
	defer func() { _ = line.conn.Close(); <-readerDone }()
	counts, inspectErr := inspectOriginalOrdinaryGitRootsV2(ctx, roots, plan)
	answer := proto.Response{ID: 1, OK: inspectErr == nil}
	if inspectErr != nil {
		answer.Error = asProtoError(inspectErr)
	} else {
		answer.Result, inspectErr = json.Marshal(counts)
	}
	if err := line.WriteFrame(proto.TResponse, answer); err != nil {
		return true, errors.Join(inspectErr, err)
	}
	if inspectErr != nil {
		return true, inspectErr
	}
	select {
	case <-readerDone:
	case <-ctx.Done():
		_ = line.conn.Close()
		<-readerDone
		return true, ctx.Err()
	}
	if releaseErr != nil {
		return true, releaseErr
	}
	if err := line.RejectAncillary(); err != nil {
		return true, err
	}
	sender, err := line.currentSender()
	if err != nil {
		return true, err
	}
	if err := line.nativePeer.CheckSender(sender); err != nil {
		return true, err
	}
	var actual ordinaryGitWorkerPlanV2
	if release.Type != proto.TPing || !api.ExactJSONFields(release.Payload, actual) || json.Unmarshal(release.Payload, &actual) != nil || actual != plan {
		return true, sessionLeaseError("the original fixed Git worker received no exact final reader release")
	}
	return true, nil
}

func acceptOrdinaryGitHelloV2(line *StartupLineV1, frame proto.Frame, role string) error {
	if err := line.AcceptNativeHelloV1(frame); err != nil {
		return err
	}
	var hello proto.Hello
	if !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil || hello.Role != role {
		return sessionLeaseError("the original Git worker has another exact native purpose")
	}
	return nil
}

func readOrdinaryGitPlanV2(line *StartupLineV1) (ordinaryGitWorkerPlanV2, error) {
	var request proto.Request
	var plan ordinaryGitWorkerPlanV2
	frame, err := line.ReadFrame()
	if err != nil {
		return plan, err
	}
	sender, err := line.currentSender()
	if err != nil {
		return plan, err
	}
	if err := line.nativePeer.CheckSender(sender); err != nil {
		return plan, err
	}
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 1 || request.Op != gitOpPurgeInspectV1 || !api.ExactJSONFields(request.Args, plan) || json.Unmarshal(request.Args, &plan) != nil || plan.Version != 2 || !supply.IsHex64(plan.Nonce) || plan.Branch == "" || strings.ContainsAny(plan.Branch, "\r\n\x00") {
		return plan, sessionLeaseError("the original Git worker received no exact fixed inspection plan")
	}
	return plan, nil
}

func inspectOriginalOrdinaryGitRootsV2(ctx context.Context, files *ordinaryActiveGitRootsV2, plan ordinaryGitWorkerPlanV2) (_ *purgeGitInspectionV1, returned error) {
	var work, admin, common *os.Root
	for _, pair := range []struct {
		file *os.File
		root **os.Root
	}{{files.Work, &work}, {files.Admin, &admin}, {files.Common, &common}} {
		info, err := pair.file.Stat()
		if err != nil || !info.IsDir() || refOf(info).UID != uint64(os.Getuid()) || info.Mode().Perm()&0o022 != 0 {
			return nil, errors.Join(sessionLeaseError("the fixed worker root loan is not an original safe directory"), err)
		}
		root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", pair.file.Fd()))
		if err != nil {
			return nil, err
		}
		*pair.root = root
		defer func() { returned = errors.Join(returned, root.Close()) }()
		if err := refOf(info).matchesRoot(root); err != nil {
			return nil, err
		}
	}
	// Explicit roots below override mutable .git and commondir selection.
	if err := exactOrdinaryGitControlV2(admin, "HEAD", []byte("ref: refs/heads/"+plan.Branch+"\n")); err != nil {
		return nil, err
	}
	if err := checkOrdinaryGitVersionV2(ctx, files); err != nil {
		return nil, err
	}
	dirty, err := runOriginalOrdinaryGitV2(ctx, files, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	unpushed, err := runOriginalOrdinaryGitV2(ctx, files, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(unpushed))
	if err != nil || count < 0 {
		return nil, sessionLeaseError("the fixed original Git worker returned an unknown commit count")
	}
	if err := exactOrdinaryGitControlV2(admin, "HEAD", []byte("ref: refs/heads/"+plan.Branch+"\n")); err != nil {
		return nil, err
	}
	return &purgeGitInspectionV1{Dirty: countLines(dirty), Unpushed: count}, ctx.Err()
}
