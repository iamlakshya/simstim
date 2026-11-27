package guest

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/proto"
)

// bundleOperation is domain state only. Every Git child belongs to the same
// canonical command runner as exec. Its hold lasts until the controller cancels
// the operation, including the interval before a full-history retry.
type bundleOperation struct {
	args    proto.CtlBundleArgs
	heads   []proto.BundleRef
	current string
	stage   string
	exclude []string
	full    bool
	ctx     context.Context
	cancel  context.CancelFunc
	release func()
	mu      sync.Mutex
	busy    bool
	ready   chan struct{}
	once    sync.Once
}

func bundleError(message string) error {
	return &proto.Error{Code: "pull_bundle", Message: message, Hint: "Commit work on a session branch, then pull again."}
}

func (g *Init) prepareBundle(a proto.CtlBundleArgs) (_ proto.CtlBundleReady, err error) {
	op, err := g.beginBundle(a)
	if err != nil {
		return proto.CtlBundleReady{}, err
	}
	return g.prepareBundleOperation(op, a)
}

// beginBundle runs synchronously on the existing ctl reader. Admission is
// recorded before an asynchronous prepare can race the following cancel.
func (g *Init) beginBundle(a proto.CtlBundleArgs) (*bundleOperation, error) {
	a.SeedHeads = slices.Clone(a.SeedHeads)
	if !api.ValidSessionID(a.ID) || len(a.ID) != 26 || !api.ValidSessionID(a.Primary) || filepath.Clean(a.Cwd) != a.Cwd || !strings.HasPrefix(a.Cwd, "/work/") {
		return nil, bundleError("invalid protected bundle workspace or identity")
	}
	if !proto.BundleRequestFits(0, a) || a.HeadCount < 0 {
		return nil, bundleError("the bundle has too many history roots")
	}
	for _, seed := range a.SeedHeads {
		if !api.ValidGitOID(seed) {
			return nil, bundleError("invalid protected seed history")
		}
	}
	g.mu.Lock()
	op := g.bundles[a.ID]
	g.mu.Unlock()
	if op == nil {
		if a.HeadCount != 0 || a.HeadsDigest != "" || a.DefaultRef != "" {
			return nil, bundleError("a retry has no existing captured operation")
		}
		release, err := g.holdCommandOperation()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		op = &bundleOperation{args: a, ctx: ctx, cancel: cancel, release: release}
		g.mu.Lock()
		if g.ended || g.bundles[a.ID] != nil {
			g.mu.Unlock()
			cancel()
			release()
			return nil, bundleError("the bundle operation is already owned or stopping")
		}
		if g.bundles == nil {
			g.bundles = map[string]*bundleOperation{}
		}
		g.bundles[a.ID] = op
		g.mu.Unlock()
		go func() { <-g.sess.Done(); g.cancelBundle(a.ID) }()
	}
	op.mu.Lock()
	if op.busy || op.ctx.Err() != nil {
		op.mu.Unlock()
		return nil, bundleError("the bundle operation is already preparing or stopping")
	}
	op.busy, op.ready = true, make(chan struct{})
	op.mu.Unlock()
	return op, nil
}

func (g *Init) prepareBundleOperation(op *bundleOperation, a proto.CtlBundleArgs) (_ proto.CtlBundleReady, err error) {
	op.mu.Lock()
	done := op.ready
	op.mu.Unlock()
	defer func() {
		op.mu.Lock()
		op.busy = false
		close(done)
		op.mu.Unlock()
		if err != nil {
			g.cancelBundle(a.ID)
		}
	}()
	// Exact output EOF may reach the controller just before serveExec removes
	// its registered command. Join that same handle before reusing the ID.
	g.mu.Lock()
	previous := g.execs[a.ID]
	g.mu.Unlock()
	if previous != nil {
		select {
		case <-previous.done:
		case <-op.ctx.Done():
			return proto.CtlBundleReady{}, op.ctx.Err()
		}
	}
	if op.stage == "" {
		if err = g.captureBundleHeads(op); err != nil {
			return proto.CtlBundleReady{}, err
		}
		if len(op.heads) == 0 {
			return proto.CtlBundleReady{ID: a.ID, Empty: true}, nil
		}
	} else {
		count, digest := proto.BundleSnapshot(op.heads)
		if !a.Full || a.Primary != op.args.Primary || a.Cwd != op.args.Cwd || !slices.Equal(a.SeedHeads, op.args.SeedHeads) || a.HeadCount != count || a.HeadsDigest != digest || a.DefaultRef != op.current {
			return proto.CtlBundleReady{}, bundleError("the full-history retry changed its captured operation")
		}
	}
	args := []string{"bundle", "create", "--progress", "-"}
	for _, head := range op.heads {
		args = append(args, head.Name)
	}
	if !a.Full && !op.full {
		for _, seed := range op.exclude {
			args = append(args, "^"+seed)
		}
	}
	if err = g.registerBundleCommand(op.ctx, bundleCommand(a.ID, g.path(op.stage), args...), bundleEnvironment()); err != nil {
		return proto.CtlBundleReady{}, err
	}
	ready := proto.CtlBundleReady{ID: a.ID, DefaultRef: op.current, Full: a.Full || op.full}
	if !proto.BundleResponseFits(0, ready) {
		return proto.CtlBundleReady{}, bundleError("the captured session refs exceed the control metadata bound")
	}
	return ready, nil
}

func (g *Init) captureBundleHeads(op *bundleOperation) error {
	if err := g.readBundleRefs(op); err != nil {
		return err
	}
	changed, err := g.bundleHasNewWork(op)
	if err != nil {
		return err
	}
	if !changed {
		op.heads = nil
		return nil
	}
	op.full, err = g.bundleNeedsFull(op)
	if err != nil {
		return err
	}
	return g.stageBundleSnapshot(op)
}

func (g *Init) readBundleRefs(op *bundleOperation) error {
	version, err := g.bundleProbe(op, op.args.Cwd, "--version")
	if err != nil {
		return err
	}
	if _, safe := gitpolicy.HookVersion(version); !safe {
		return bundleError("the guest Git cannot safely disable every configured hook")
	}
	prefix := "refs/heads/" + brand.Name + "/" + op.args.Primary + "/"
	data, err := g.bundleProbe(op, op.args.Cwd, "for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil {
		return err
	}
	op.heads, err = parseBundleHeads(data, prefix)
	if err != nil {
		return err
	}
	current, err := g.bundleProbe(op, op.args.Cwd, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return bundleError("the guest workspace has no current session branch")
	}
	op.current = strings.TrimSuffix(current, "\n")
	selected := false
	for _, head := range op.heads {
		selected = selected || head.Name == op.current
		if _, err = g.bundleProbe(op, op.args.Cwd, "check-ref-format", head.Name); err != nil {
			return bundleError("a session branch has an invalid ref name")
		}
	}
	if !selected {
		return bundleError("the guest current branch is outside its session namespace")
	}
	return nil
}

func (g *Init) bundleHasNewWork(op *bundleOperation) (bool, error) {
	for _, seed := range op.args.SeedHeads {
		if _, err := g.bundleProbe(op, op.args.Cwd, "cat-file", "-e", seed+"^{commit}"); err == nil {
			op.exclude = append(op.exclude, seed)
		} else if op.ctx.Err() != nil {
			return false, op.ctx.Err()
		}
	}
	countArgs := []string{"rev-list", "--count"}
	for _, head := range op.heads {
		countArgs = append(countArgs, head.OID)
	}
	for _, seed := range op.exclude {
		countArgs = append(countArgs, "^"+seed)
	}
	count, err := g.bundleProbe(op, op.args.Cwd, countArgs...)
	if err != nil {
		return false, err
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(count, "\n"), 10, 64)
	if err != nil {
		return false, bundleError("the guest Git returned an invalid commit count")
	}
	return n != 0, nil
}

func (g *Init) bundleNeedsFull(op *bundleOperation) (bool, error) {
	if len(op.exclude) == 0 {
		return true, nil
	}
	// Native incremental bundles may omit a ref whose entire tip is
	// excluded. A mixed snapshot therefore uses full history so its complete
	// advertised refs and default branch remain stable across a retry.
	for _, head := range op.heads {
		check := []string{"rev-list", "--max-count=1", head.OID}
		for _, seed := range op.exclude {
			check = append(check, "^"+seed)
		}
		one, err := g.bundleProbe(op, op.args.Cwd, check...)
		if err != nil {
			return false, err
		}
		one = strings.TrimSuffix(one, "\n")
		if one == "" {
			return true, nil
		}
		if !api.ValidGitOID(one) {
			return false, bundleError("the guest Git returned an invalid branch revision")
		}
	}
	return false, nil
}

func (g *Init) stageBundleSnapshot(op *bundleOperation) error {
	format, err := g.bundleProbe(op, op.args.Cwd, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	format = strings.TrimSuffix(format, "\n")
	if format != "sha1" && format != "sha256" {
		return bundleError("the guest Git object format is not supported")
	}
	nonce, err := bundleNonce()
	if err != nil {
		return err
	}
	op.stage = filepath.Join("/tmp", brand.Name+"-pull-"+nonce)
	// Git creates this unpredictable fresh path as AgentUID, through the
	// canonical runner. The init never lends root execution to guest Git.
	if _, err = os.Lstat(g.path(op.stage)); !os.IsNotExist(err) {
		return bundleError("the fresh guest bundle path already exists")
	}
	if _, err = g.bundleProbe(op, "/tmp", "init", "--bare", "--template=", "--object-format="+format, "--", g.path(op.stage)); err != nil {
		return err
	}
	fetch := []string{"-c", "protocol.file.allow=always", "fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", "--", g.path(op.args.Cwd)}
	for _, head := range op.heads {
		fetch = append(fetch, head.OID+":"+head.Name)
	}
	for _, seed := range op.exclude {
		fetch = append(fetch, seed)
	}
	_, err = g.bundleProbe(op, op.stage, fetch...)
	return err
}

func (g *Init) cancelBundle(id string) {
	g.mu.Lock()
	op := g.bundles[id]
	g.mu.Unlock()
	if op == nil {
		return
	}
	op.once.Do(func() {
		op.cancel()
		op.mu.Lock()
		done := op.ready
		op.mu.Unlock()
		if done != nil {
			<-done
		}
		g.mu.Lock()
		run := g.execs[id]
		g.mu.Unlock()
		if run != nil {
			run.cancel()
			<-run.done
		}
		if op.stage != "" {
			_ = os.RemoveAll(g.path(op.stage))
		}
		g.mu.Lock()
		delete(g.bundles, id)
		g.mu.Unlock()
		op.release()
	})
}

func (g *Init) stopBundles() {
	g.mu.Lock()
	var ids []string
	for id := range g.bundles {
		ids = append(ids, id)
	}
	g.mu.Unlock()
	for _, id := range ids {
		g.cancelBundle(id)
	}
}

func bundleNonce() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data[:])), nil
}

func bundleCommand(id, cwd string, args ...string) proto.CtlExecArgs {
	argv := append([]string{"/usr/bin/git"}, gitpolicy.Args()...)
	argv = append(argv, "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true", "--no-optional-locks", "-C", cwd)
	return proto.CtlExecArgs{ID: id, Cwd: cwd, Argv: append(argv, args...)}
}

func bundleEnvironment() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "XDG_CONFIG_HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=file", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_LITERAL_PATHSPECS=1", "LC_ALL=C"}
}

func (g *Init) bundleProbe(op *bundleOperation, cwd string, args ...string) (string, error) {
	id, err := bundleNonce()
	if err != nil {
		return "", err
	}
	out, diagnostics := &bundleText{remaining: 1 << 20}, &bundleText{remaining: 64 << 10}
	code, err := g.runBundleProbe(op.ctx, bundleCommand(id, g.path(cwd), args...), bundleEnvironment(), out, diagnostics)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", bundleError(fmt.Sprintf("the guest bundle Git command ended with status %d", code))
	}
	return out.data.String(), nil
}

type bundleText struct {
	data      strings.Builder
	remaining int
}

func (b *bundleText) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, io.ErrShortBuffer
	}
	b.remaining -= len(p)
	return b.data.Write(p)
}

func parseBundleHeads(data, prefix string) ([]proto.BundleRef, error) {
	var result []proto.BundleRef
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		if line == "" {
			continue
		}
		oid, name, ok := strings.Cut(line, " ")
		if !ok || !api.ValidGitOID(oid) || !utf8.ValidString(name) || !strings.HasPrefix(name, prefix) || name == prefix || strings.ContainsAny(name, "\x00\r\n ") || seen[name] {
			return nil, bundleError("the guest Git returned invalid session refs")
		}
		seen[name] = true
		result = append(result, proto.BundleRef{Name: name, OID: oid})
	}
	slices.SortFunc(result, func(a, b proto.BundleRef) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}
