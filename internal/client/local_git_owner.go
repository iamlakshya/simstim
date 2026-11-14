package client

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
)

const localGitControlMax = 4 << 20

type localGitRequest struct {
	Executable      string
	ExecutableID    string
	Directory       string
	Args            []string
	Env             []string
	Role            localGitRole
	HelperEnv       []string `json:"-"` // parent-only explicit startup metadata, never inherited wholesale
	HelperDirectory string   `json:"-"` // actual original command directory, not a source grant
}

type localGitStatus struct {
	Ready      bool
	Quiescent  bool
	Code       int
	Failure    string
	NativeDone bool // Windows native exit; Job accounting remains independent
}

// Tests may observe actual closed helper startup. This hook grants no source,
// namespace, environment or launch permission and is nil in the product.
var localGitHelperObservation func(string)

// Each copied pullGit shares this ownership set. It is neither a process
// registry nor source authority. Captures may be deleted only after every
// operation which could still use them has positively reported quiescence.
type localGitOwnership struct {
	mu      sync.Mutex
	pending []*localGitOwner
}

func (o *localGitOwnership) retain(owner *localGitOwner) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pending = append(o.pending, owner)
}

func (o *localGitOwnership) quiescent() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, owner := range o.pending {
		select {
		case <-owner.done:
			if !owner.known {
				return &localGitIncomplete{owner: owner}
			}
		default:
			return &localGitIncomplete{owner: owner}
		}
	}
	for _, owner := range o.pending {
		if owner.source != nil {
			if err := owner.source.Close(); err != nil {
				return fmt.Errorf("close the retained local Git source: %w", err)
			}
			owner.source = nil
		}
	}
	o.pending = nil
	return nil
}

type localGitIncomplete struct{ owner *localGitOwner }

func (e *localGitIncomplete) Error() string {
	return "local Git cleanup is incomplete; its owner and input captures are retained. Try: wait for cleanup before removing the captures or trying again"
}

// localGitSink prevents an unfinished native operation from changing a caller's
// output after runIO returns. The actual copier remains joined by owner.done.
type localGitSink struct {
	mu     sync.Mutex
	target io.Writer
	closed bool
}

func (s *localGitSink) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	return s.target.Write(data)
}

func (s *localGitSink) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

type localGitOwner struct {
	command    *exec.Cmd
	control    *os.File
	done       chan struct{}
	ready      chan error
	uncertain  chan struct{}
	stdout     *localGitSink
	stderr     *localGitSink
	known      bool // published by closing done, never read before done
	code       int
	err        error
	close      sync.Once
	source     LocalGitSource // retained on incomplete cleanup, closed by ownership
	nativeStop func()         // the platform's actual owned object, never a numeric PID
}

func (o *localGitOwner) cancel() {
	o.close.Do(func() {
		_ = o.control.Close()
		if o.nativeStop != nil {
			o.nativeStop()
		}
	})
}

func (o *localGitOwner) finishControl() {
	// The platform has independently proved absence. A racing cancellation
	// either finishes first or sees this joined close; no late handle action.
	o.close.Do(func() { _ = o.control.Close() })
}

func (o *localGitOwner) incomplete() error {
	o.cancel()
	o.stdout.close()
	o.stderr.close()
	return &localGitIncomplete{owner: o}
}

func writeLocalGitRequest(out io.Writer, request localGitRequest) error {
	if !localGitRecipe(request.Args) || validateGitRequestRole(request) != nil {
		return gitMetadataFailure()
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode local Git launch: %w", err)
	}
	if len(data) > localGitControlMax {
		return fmt.Errorf("local Git launch metadata exceeds its control bound")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	if _, err := out.Write(size[:]); err != nil {
		return fmt.Errorf("write local Git launch size: %w", err)
	}
	if _, err := out.Write(data); err != nil {
		return fmt.Errorf("write local Git launch: %w", err)
	}
	return nil
}

func readLocalGitRequest(input io.Reader) (localGitRequest, error) {
	var request localGitRequest
	var size [4]byte
	if _, err := io.ReadFull(input, size[:]); err != nil {
		return request, fmt.Errorf("read local Git launch size: %w", err)
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > localGitControlMax {
		return request, fmt.Errorf("invalid local Git launch size")
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(input, data); err != nil {
		return request, fmt.Errorf("read local Git launch: %w", err)
	}
	fields, err := api.JSONObjectFields(data, "Executable", "ExecutableID", "Directory", "Args", "Env", "Role")
	if err != nil || len(fields) != 6 {
		return request, gitMetadataFailure()
	}
	for name := range fields {
		if name != "Executable" && name != "ExecutableID" && name != "Directory" && name != "Args" && name != "Env" && name != "Role" {
			return request, gitMetadataFailure()
		}
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, fmt.Errorf("decode local Git launch: %w", err)
	}
	if !localGitRecipe(request.Args) {
		return request, fmt.Errorf("the local Git operation is not a closed recipe")
	}
	if err := validateGitRequestRole(request); err != nil {
		return request, err
	}
	return request, nil
}

func runLocalGitOwner(ctx context.Context, request localGitRequest, input io.Reader, output, diagnostic io.Writer, source LocalGitSource) (int, *localGitOwner, error) {
	if err := ctx.Err(); err != nil {
		return -1, nil, err
	}
	if !localGitRecipe(request.Args) {
		return -1, nil, &UserError{Code: "git_operation", Msg: "this local Git operation has no closed recipe", Hint: "Use the canonical local Git operation.", Kind: KindRefused}
	}
	if err := validateGitRequestRole(request); err != nil {
		return -1, nil, err
	}
	if err := validateGitRoleEnvironment(localGitHelper, request.HelperEnv, runtime.GOOS == "windows"); err != nil {
		return -1, nil, err
	}
	owner, err := startLocalGitOwner(request, input, output, diagnostic)
	if err != nil {
		return -1, nil, err
	}
	stopCancel := context.AfterFunc(ctx, owner.cancel)
	defer stopCancel()
	// No GO means no Git. The guardian validates its complete request before
	// READY; the same original source is checked once more before releasing GO.
	if err = writeLocalGitRequest(owner.control, request); err == nil {
		select {
		case err = <-owner.ready:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err == nil {
		err = source.Revalidate(ctx)
	}
	if err == nil {
		_, err = owner.control.Write([]byte{'G'})
	}
	if err != nil {
		owner.cancel()
	}
	select {
	case <-owner.done:
		if !owner.known {
			return -1, owner, errors.Join(err, ctx.Err(), owner.incomplete())
		}
		return owner.code, nil, errors.Join(err, localGitResultError(owner), ctx.Err())
	case <-owner.uncertain:
		return -1, owner, errors.Join(err, ctx.Err(), owner.incomplete())
	case <-ctx.Done():
		owner.cancel()
		select {
		case <-owner.done:
			if owner.known {
				return owner.code, nil, errors.Join(err, localGitResultError(owner), ctx.Err())
			}
		default:
		}
		grace := localGitCancelGrace()
		if grace <= 0 {
			return -1, owner, errors.Join(err, ctx.Err(), owner.incomplete())
		}
		// This bounds the caller wait, not kernel cleanup. The actual guardian
		// and its source handles stay owned if native work cannot quiesce.
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-owner.done:
			if owner.known {
				return owner.code, nil, errors.Join(err, localGitResultError(owner), ctx.Err())
			}
		case <-timer.C:
		}
		return -1, owner, errors.Join(err, ctx.Err(), owner.incomplete())
	}
}

func localGitExecutable(name, expected string) (*os.File, error) {
	environment, err := helperGitMetadataEnvironment()
	if err != nil {
		return nil, err
	}
	path := ""
	for _, entry := range environment {
		if key, value, valid := strings.Cut(entry, "="); valid && key == "PATH" {
			path = value
		}
	}
	fixed, err := resolveGitMetadataPath(context.Background(), path, runtime.GOOS == "windows")
	if err != nil || !filepath.IsAbs(name) || expected == "" {
		return nil, gitMetadataFailure()
	}
	actual, err := os.Stat(fixed)
	if err != nil || !actual.Mode().IsRegular() {
		return nil, gitMetadataFailure()
	}
	requested, err := os.Stat(name)
	if err != nil || !os.SameFile(actual, requested) {
		return nil, gitMetadataFailure()
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, gitMetadataFailure()
	}
	if err := revalidateGitExecutable(name, expected, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func revalidateGitExecutable(name, expected string, pin *os.File) error {
	if pin == nil || expected == "" {
		return gitMetadataFailure()
	}
	held, err := files.Identity(pin)
	if err != nil || held != expected {
		return gitMetadataFailure()
	}
	current, err := os.Open(name)
	if err != nil {
		return gitMetadataFailure()
	}
	identity, identityErr := files.Identity(current)
	closeErr := current.Close()
	if identityErr != nil || closeErr != nil || identity != expected {
		return gitMetadataFailure()
	}
	return nil
}

func localGitArguments(request localGitRequest) []string {
	safe := append(gitpolicy.Args(), "-c", "gc.auto=0", "-c", "maintenance.auto=false", "--no-pager", "--literal-pathspecs", "-C", request.Directory)
	return append(safe, request.Args...)
}

func sendLocalGitStatus(status *os.File, value localGitStatus) {
	// A failed status write never ends native cleanup. A closed parent pipe is
	// ordinary parent death; the actual platform owner must still drain work.
	_ = json.NewEncoder(status).Encode(value)
}

func localGitResultError(owner *localGitOwner) error {
	if owner.err != nil {
		return owner.err
	}
	if owner.code != 0 {
		return fmt.Errorf("native Git exited with status %d", owner.code)
	}
	return nil
}
