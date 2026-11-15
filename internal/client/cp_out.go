package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client/review"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

// CopyOptions contains only explicit local replacement and review choices.
type CopyOptions struct{ Force, AllowFlagged, Terminal bool }

// CopyResult reports names and counts, never copied content or secret values.
type CopyResult struct {
	Files       int              `json:"files"`
	Directories int              `json:"directories"`
	Bytes       int64            `json:"bytes"`
	Skipped     []string         `json:"skipped,omitempty"`
	Findings    []review.Finding `json:"findings,omitempty"`
}

// CopyReviewError retains the safe findings for an explicit terminal answer.
type CopyReviewError struct {
	Result  CopyResult
	Failure *UserError
}

// Error returns the canonical refusal message.
func (e *CopyReviewError) Error() string { return e.Failure.Error() }

// Unwrap exposes its kind, code and retry hint.
func (e *CopyReviewError) Unwrap() error { return e.Failure }

type copyOutputFile struct {
	entry  proto.CopyListEntryV1
	target filecopy.Target
	remote string
}

// CopyOut retains one actual source/receipt/settings operation and its staged
// files through terminal questions. Close is required on every outcome.
type CopyOut struct {
	original                      context.Context
	request                       *GrantRequest
	connection                    *Conn
	id, remote                    string
	options                       CopyOptions
	result                        CopyResult
	plan                          *localReviewValuePlan
	public                        []api.Secret
	metadata                      *localGitMetadata
	guard                         LocalGitSource
	batch                         *filecopy.Batch
	files                         []copyOutputFile
	targets                       []filecopy.Target
	destinations                  []string
	downloaded, committed, closed bool
	stop                          func() bool
}

func copyClientError(code, message, hint string) error {
	return &UserError{Code: code, Msg: message, Hint: hint, Kind: KindRefused}
}

func copyClientFailure(err error) error {
	if err == nil {
		return nil
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	var core *filecopy.Error
	if errors.As(err, &core) {
		return &UserError{Code: core.Code, Msg: core.Message, Hint: core.Hint, Kind: KindRefused}
	}
	return err
}

func (c *Conn) dedicatedCopy(ctx context.Context) (*Conn, error) {
	if c == nil || c.agentOnly || !c.HasFeature(proto.FeatureFileTransferV1) {
		return nil, copyClientError("helper_old", "the host helper cannot copy sandbox files", "Update the host helper, then resume the sandbox.")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	owned, err := dial(WithoutWake(WithoutInstall(ctx)), cloneGrantHost(c.Host), c.route)
	if err != nil {
		return nil, err
	}
	if owned.HostID() != c.HostID() || owned.HostIDChanged() || !sameHostTransportSource(owned.hostTransport.source, c.hostTransport.source) {
		owned.Close()
		return nil, copyClientError("cp_identity", "the dedicated copy connection changed its original host source", "Reconnect to the original host before copying.")
	}
	if err := bindRequestTransportSource(ctx, owned); err != nil {
		owned.Close()
		return nil, err
	}
	return owned, nil
}

// PrepareCopyOut reads metadata and checks every destination before creating
// anything. Its actual local value plan has no command until Publish.
func (c *Conn) PrepareCopyOut(ctx context.Context, id, remote, local string, options CopyOptions) (_ *CopyOut, err error) {
	defer func() { err = copyClientFailure(err) }()
	if !api.ValidSessionID(id) {
		return nil, copyClientError("cp_path", "the copy has no exact session ID", "List sessions and use the full address.")
	}
	request := grantRequestOf(ctx)
	if request == nil {
		request = NewReviewGrantRequest()
		ctx = WithGrantRequest(ctx, request)
	}
	if requestGrantPurpose(request) != grantLocalReview {
		return nil, grantPurposeError()
	}
	owned, err := c.dedicatedCopy(ctx)
	if err != nil {
		return nil, err
	}
	op := &CopyOut{original: ctx, request: request, connection: owned, id: id, options: options}
	op.stop = context.AfterFunc(ctx, owned.Close)
	defer func() {
		if err != nil {
			err = errors.Join(err, op.Close())
		}
	}()
	top, relative, err := filecopy.GuestPath(remote)
	if err != nil {
		return nil, err
	}
	op.remote = path.Join(top, relative)
	var raw json.RawMessage
	if err := owned.Call(ctx, "cp.list", proto.CopyListArgsV1{Version: proto.FileTransferVersionV1, ID: id, Path: op.remote}, &raw); err != nil {
		return nil, err
	}
	listing, err := checkedCopyListing(raw, op.remote)
	if err != nil {
		return nil, err
	}
	op.public, err = owned.ReviewPublicStandIns(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := getSandboxReceiptPolicyContext(ctx, owned, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.deliveryReceipt == nil {
		return nil, reviewAuthorityError()
	}
	op.plan, err = prepareLocalReviewValuePlan(ctx, owned, id, p.deliveryReceipt.Allowed, p.deliveryReceipt.Missing)
	if err != nil {
		return nil, err
	}
	current, err := owned.Policy(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, err := owned.copyLimit(ctx, id, &current, op.plan.operation)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, entry := range listing.Entries {
		if entry.Mode == "file" {
			if entry.Size > limit-total {
				return nil, copyClientError("cp_too_big", "the selected copy exceeds sandbox.cp_max", "Copy fewer bytes or raise the original local copy ceiling.")
			}
			total += entry.Size
		}
	}
	lease, err := newOrdinaryLocalGitMetadataSource(ctx, op.plan.operation)
	if err != nil {
		return nil, err
	}
	op.metadata, err = captureGitMetadata(ctx, lease, os.LookupEnv, runtime.GOOS == "windows")
	if err != nil {
		return nil, err
	}
	environment, err := op.metadata.environment(ctx, localGitNormal, "", api.GitIdentity{})
	if err != nil {
		return nil, err
	}
	parent, leaf, err := filecopy.OpenLocalParent(local)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	prefix := leaf
	if info, statErr := parent.Lstat(leaf); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		prefix = path.Join(leaf, listing.Name)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	op.guard, err = openLocalSource(ctx, parent.Name(), false, environment)
	if err != nil {
		return nil, err
	}
	if listing.Directory {
		op.targets = append(op.targets, filecopy.Target{Path: prefix, Directory: true})
		op.result.Directories++
	}
	for _, entry := range listing.Entries {
		if entry.Mode == "symlink" || entry.Mode == "special" {
			op.result.Skipped = append(op.result.Skipped, entry.Path)
			continue
		}
		targetPath := prefix
		if listing.Directory {
			targetPath = path.Join(prefix, entry.Path)
		}
		target := filecopy.Target{Path: targetPath, Size: entry.Size, Directory: entry.Directory}
		op.targets = append(op.targets, target)
		if entry.Directory {
			op.result.Directories++
			continue
		}
		name := op.remote
		if listing.Directory {
			name = path.Join(op.remote, entry.Path)
		}
		op.files = append(op.files, copyOutputFile{entry: entry, target: target, remote: name})
		op.result.Files++
		op.result.Bytes += entry.Size
	}
	for _, target := range op.targets {
		op.destinations = append(op.destinations, filepath.Join(parent.Name(), filepath.FromSlash(target.Path)))
	}
	checker, ok := op.guard.(interface {
		CheckDestinations(context.Context, []string) error
	})
	if !ok {
		return nil, copyPolicyUnknown()
	}
	if err := checker.CheckDestinations(ctx, op.destinations); err != nil {
		return nil, err
	}
	op.batch, err = filecopy.NewBatch(parent, op.targets, options.Force)
	if err != nil {
		return nil, err
	}
	scanner, err := copyScanner(nil)
	if err != nil {
		return nil, err
	}
	defer scanner.Clear()
	for _, file := range op.files {
		findings, err := copyScan(scanner, file.target.Path, file.entry.Size, nil, nil)
		if err != nil {
			return nil, err
		}
		op.result.Findings = append(op.result.Findings, findings...)
	}
	op.result.Findings = uniqueCopyFindings(op.result.Findings)
	return op, nil
}

func checkedCopyListing(raw []byte, selected string) (proto.CopyListResultV1, error) {
	var result proto.CopyListResultV1
	fields, err := api.JSONObjectFields(raw, "v", "name", "directory", "entries")
	if err != nil || len(fields) != 4 || len(raw) > 1<<20 || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != proto.FileTransferVersionV1 || result.Name != path.Base(selected) || filecopy.Name(result.Name) != nil || len(result.Entries) > 10000 {
		return result, copyClientError("cp_path", "the host returned an invalid copy listing", "Copy an ordinary folder with at most 10000 entries.")
	}
	seen := map[string]bool{}
	for _, entry := range result.Entries {
		if filecopy.Relative(entry.Path) != nil || seen[strings.ToLower(entry.Path)] || entry.Directory != (entry.Mode == "directory") || entry.Size < 0 {
			return result, copyClientError("cp_path", "the host returned unsafe or colliding copy names", "Copy paths without links, dot-dot or Git metadata.")
		}
		seen[strings.ToLower(entry.Path)] = true
		switch entry.Mode {
		case "file":
		case "directory", "symlink", "special":
			if entry.Size != 0 {
				return result, copyClientError("cp_path", "a non-file declares copy bytes", "Update the host helper.")
			}
		default:
			return result, copyClientError("cp_path", "the host returned an unknown copy type", "Update the host helper.")
		}
	}
	if !result.Directory && (len(result.Entries) != 1 || result.Entries[0].Path != result.Name) {
		return result, copyClientError("cp_path", "the selected file changed its listing shape", "Repeat the copy with the current helper.")
	}
	return result, nil
}

// Preview returns owned safe names/counts for the original selected copy.
func (o *CopyOut) Preview() CopyResult {
	if o == nil {
		return CopyResult{}
	}
	result := o.result
	result.Skipped = slices.Clone(result.Skipped)
	result.Findings = slices.Clone(result.Findings)
	return result
}

// AllowFlagged records the caller's explicit review choice for this operation.
func (o *CopyOut) AllowFlagged() { o.options.AllowFlagged = true }

func (o *CopyOut) check(ctx context.Context) error {
	if o == nil || o.closed || o.committed {
		return copyClientError("cp_closed", "the copy operation is closed", "Start a new copy command.")
	}
	if err := o.original.Err(); err != nil {
		return err
	}
	if err := o.plan.checkLocal(ctx); err != nil {
		return err
	}
	if err := o.metadata.Revalidate(ctx); err != nil {
		return err
	}
	if err := o.guard.Revalidate(ctx); err != nil {
		return err
	}
	checker, ok := o.guard.(interface {
		CheckDestinations(context.Context, []string) error
	})
	if !ok {
		return copyPolicyUnknown()
	}
	return checker.CheckDestinations(ctx, o.destinations)
}

func (o *CopyOut) localContext(ctx context.Context) (context.Context, error) {
	if o == nil {
		return ctx, copyPolicyUnknown()
	}
	if selected := grantRequestOf(ctx); selected != nil && selected != o.request {
		return ctx, grantPurposeError()
	}
	return WithGrantRequest(ctx, o.request), nil
}

// Download stages all files after the original path review, without publishing.
func (o *CopyOut) Download(ctx context.Context) (returned error) {
	defer func() { returned = copyClientFailure(returned) }()
	var err error
	ctx, err = o.localContext(ctx)
	if err != nil {
		return err
	}
	if err := o.check(ctx); err != nil {
		return err
	}
	if o.downloaded {
		return nil
	}
	if err := copyConsent(o.result, o.options.AllowFlagged); err != nil {
		return err
	}
	for _, file := range o.files {
		if err := o.batch.Stage(ctx, file.target, &copyChunkReader{ctx: ctx, connection: o.connection, id: o.id, path: file.remote, size: file.entry.Size}); err != nil {
			return err
		}
	}
	o.downloaded = true
	return nil
}

// Publish fetches fresh local review values and scans every received file before
// publishing. Retrying a terminal question retains this SAME original plan.
func (o *CopyOut) Publish(ctx context.Context) (result CopyResult, err error) {
	defer func() { err = copyClientFailure(err) }()
	ctx, err = o.localContext(ctx)
	if err != nil {
		return o.Preview(), err
	}
	if err := o.check(ctx); err != nil {
		return o.Preview(), err
	}
	if !o.downloaded {
		return o.Preview(), copyClientError("cp_incomplete", "the selected files have not all arrived", "Finish the checked download before publishing it.")
	}
	var values [][]byte
	if len(o.files) > 0 {
		literal, err := o.plan.read(ctx, o.options.Terminal)
		if err != nil {
			return o.Preview(), err
		}
		for _, value := range literal {
			values = append(values, []byte(string(value)))
		}
		for _, value := range o.public {
			values = append(values, []byte(string(value)))
		}
	}
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	scanner, err := copyScanner(values)
	if err != nil {
		return o.Preview(), err
	}
	defer scanner.Clear()
	findings := slices.Clone(o.result.Findings)
	for _, file := range o.files {
		var data []byte
		if file.entry.Size <= review.FileLimit {
			data, err = o.batch.ReadStaged(ctx, file.target.Path, review.FileLimit)
			if err != nil {
				return o.Preview(), err
			}
		}
		var modules [][]byte
		if strings.EqualFold(path.Base(file.target.Path), ".gitmodules") && len(data) > 0 {
			modules, err = o.parseModules(ctx, data)
			if err != nil {
				return o.Preview(), err
			}
		}
		found, err := copyScan(scanner, file.target.Path, file.entry.Size, data, modules)
		if err != nil {
			return o.Preview(), err
		}
		findings = append(findings, found...)
	}
	o.result.Findings = uniqueCopyFindings(findings)
	if err := copyConsent(o.result, o.options.AllowFlagged); err != nil {
		return o.Preview(), err
	}
	if err := o.check(ctx); err != nil {
		return o.Preview(), err
	}
	if err := o.batch.Commit(ctx); err != nil {
		return o.Preview(), err
	}
	o.committed = true
	return o.Preview(), nil
}

// Close cancels the dedicated connection and releases only owned local stages.
func (o *CopyOut) Close() error {
	if o == nil || o.closed {
		return nil
	}
	o.closed = true
	if o.stop != nil {
		o.stop()
	}
	if o.connection != nil {
		o.connection.Close()
	}
	var failures []error
	if o.batch != nil {
		failures = append(failures, o.batch.Close())
	}
	if o.guard != nil {
		failures = append(failures, o.guard.Close())
	}
	if o.metadata != nil {
		failures = append(failures, o.metadata.Close())
	}
	if o.plan != nil {
		failures = append(failures, o.plan.operation.Close())
	}
	return errors.Join(failures...)
}
