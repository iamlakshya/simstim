package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

type copyInputFile struct {
	name, target, hash string
	size               int64
	directory          bool
	info               os.FileInfo
}

// CopyIn retains the actual original local source/settings selection through
// streaming. Its source descriptors and operation must be closed on all paths.
type CopyIn struct {
	original          context.Context
	connection        *Conn
	id                string
	operation         *settingsOperation
	root              *os.Root
	files             []copyInputFile
	folders           []privateFolder
	options           CopyOptions
	result            CopyResult
	closed, published bool
	stop              func() bool
}

// PrepareCopyIn checks the original approved cap before reading laptop files,
// then seals every no-follow ordinary source through the canonical paste guard.
func (c *Conn) PrepareCopyIn(ctx context.Context, id, local, remote string, options CopyOptions) (_ *CopyIn, err error) {
	defer func() { err = copyClientFailure(err) }()
	if !api.ValidSessionID(id) {
		return nil, copyClientError("cp_path", "the input copy has no exact session ID", "List sessions and use the full address.")
	}
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return nil, err
	}
	owned, err := c.dedicatedCopy(ctx)
	if err != nil {
		return nil, err
	}
	op := &CopyIn{original: ctx, connection: owned, id: id, options: options}
	op.stop = context.AfterFunc(ctx, owned.Close)
	defer func() {
		if err != nil {
			err = errors.Join(err, op.Close())
		}
	}()
	op.operation, err = openSettingsOperation(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := op.operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := NormaliseSettingsSecrets(settings); err != nil {
		return nil, err
	}
	policy, err := owned.Policy(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, err := owned.copyLimit(ctx, id, &policy, op.operation)
	if err != nil {
		return nil, err
	}
	op.folders = privateFolderInfos()
	top, relative, err := filecopy.GuestPath(remote)
	if err != nil {
		return nil, err
	}
	destination := path.Join(top, relative)
	parent, leaf, err := filecopy.OpenLocalParent(local)
	if err != nil {
		return nil, err
	}
	op.root = parent
	entries, isDirectory, err := filecopy.List(ctx, parent, leaf, 10000)
	if err != nil {
		return nil, err
	}
	if relative == "." || strings.HasSuffix(remote, "/") {
		destination = path.Join(destination, leaf)
	}
	var total int64
	for _, entry := range entries {
		if entry.Kind != "file" && entry.Kind != "directory" {
			return nil, copyClientError("cp_path", "a laptop copy source contains a link or special file", "Choose ordinary local files and folders without links.")
		}
		if entry.Size > limit-total {
			return nil, copyClientError("cp_too_big", "the local copy exceeds sandbox.cp_max", "Select fewer bytes or raise the original local copy ceiling.")
		}
		total += entry.Size
	}
	if isDirectory {
		info, err := parent.Lstat(leaf)
		if err != nil {
			return nil, err
		}
		selected := copyInputFile{name: leaf, target: destination, directory: true, info: info}
		if err := op.checkDirectory(selected); err != nil {
			return nil, err
		}
		op.files = append(op.files, selected)
		op.result.Directories++
	}
	for _, entry := range entries {
		name := leaf
		target := destination
		if isDirectory {
			name = path.Join(leaf, entry.Path)
			target = path.Join(destination, entry.Path)
		}
		if _, _, err := filecopy.GuestPath(target); err != nil {
			return nil, err
		}
		selected := copyInputFile{name: name, target: target, size: entry.Size, directory: entry.Kind == "directory", info: entry.Info}
		if err := op.operation.Revalidate(ctx); err != nil {
			return nil, err
		}
		if selected.directory {
			if err := op.checkDirectory(selected); err != nil {
				return nil, err
			}
			op.result.Directories++
		} else {
			file, err := files.OpenRegular(parent, name)
			if err != nil {
				return nil, err
			}
			selected.hash, err = op.checkAndHash(ctx, selected, file)
			err = errors.Join(err, file.Close())
			if err != nil {
				return nil, err
			}
			op.result.Files++
			op.result.Bytes += entry.Size
		}
		op.files = append(op.files, selected)
	}
	if err := op.check(ctx); err != nil {
		return nil, err
	}
	return op, nil
}

func (o *CopyIn) check(ctx context.Context) error {
	if o == nil || o.closed || o.published {
		return copyClientError("cp_closed", "the input copy is closed", "Start a new copy command.")
	}
	if err := o.original.Err(); err != nil {
		return err
	}
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return err
	}
	if err := o.operation.Revalidate(ctx); err != nil {
		return err
	}
	return bindRequestTransportSource(ctx, o.connection)
}

func (o *CopyIn) checkDirectory(file copyInputFile) (returned error) {
	held, err := filecopy.OpenDirectory(o.root, file.name)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, held.Close()) }()
	actual, err := held.Stat()
	if err != nil || !os.SameFile(actual, file.info) {
		return copyClientError("copy_changed", "the selected local directory changed", "Repeat the copy from a stable source.")
	}
	return checkCopyPath(o.root, file.name, filepath.Join(o.root.Name(), filepath.FromSlash(file.name)), held, o.folders)
}

func (o *CopyIn) checkAndHash(ctx context.Context, file copyInputFile, input *os.File) (string, error) {
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, file.info) || info.Size() != file.size {
		return "", copyInputError("copy_changed", "the selected source changed before reading")
	}
	if err := checkCopyPath(o.root, file.name, filepath.Join(o.root.Name(), filepath.FromSlash(file.name)), input, o.folders); err != nil {
		return "", err
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &inputContextReader{ctx: ctx, reader: io.LimitReader(input, file.size+1)})
	if err != nil {
		return "", err
	}
	after, err := input.Stat()
	if err != nil || n != file.size || after.Size() != file.size || !after.ModTime().Equal(info.ModTime()) {
		return "", copyInputError("copy_changed", "the local source changed while sealing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Publish streams the exact original files over the existing versioned upload
// frames. Every first header passes the actual host publication gate.
func (o *CopyIn) Publish(ctx context.Context) (result CopyResult, returned error) {
	defer func() { returned = copyClientFailure(returned) }()
	if o == nil {
		return CopyResult{}, copyPolicyUnknown()
	}
	if err := o.check(ctx); err != nil {
		return o.result, err
	}
	for _, file := range o.files {
		if err := o.upload(ctx, file); err != nil {
			o.connection.Close()
			return o.result, err
		}
	}
	o.published = true
	return o.result, nil
}

func (o *CopyIn) upload(ctx context.Context, file copyInputFile) (returned error) {
	if err := o.check(ctx); err != nil {
		return err
	}
	var input *os.File
	if file.directory {
		if err := o.checkDirectory(file); err != nil {
			return err
		}
	} else {
		var err error
		input, err = files.OpenRegular(o.root, file.name)
		if err != nil {
			return err
		}
		defer func() { returned = errors.Join(returned, input.Close()) }()
		hash, err := o.checkAndHash(ctx, file, input)
		if err != nil {
			return err
		}
		if hash != file.hash {
			return copyInputError("copy_changed", "the selected source bytes changed after approval")
		}
		if _, err := input.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	c := o.connection
	turn := c.uploadTurn()
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-turn }()
	owned, release, err := c.beginGrant(ctx, "copy files in", true)
	if err != nil {
		return err
	}
	defer release()
	if err := o.operation.Revalidate(owned); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	response := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.pending[id] = response
	c.mu.Unlock()
	failed := true
	defer func() {
		if failed {
			c.drop(id)
		}
	}()
	begin := proto.UploadBegin{ID: id, Session: o.id, Name: path.Base(file.name), Size: file.size, Path: file.target, Replace: o.options.Force && !file.directory, Directory: file.directory}
	if err := c.w.WriteJSONCtx(owned, proto.TUploadBegin, begin); err != nil {
		return err
	}
	release()
	hash := sha256.New()
	if !file.directory {
		buffer := make([]byte, 256<<10)
		var offset int64
		for offset < file.size {
			if err := o.check(ctx); err != nil {
				return err
			}
			n, err := io.ReadFull(input, buffer[:min(int64(len(buffer)), file.size-offset)])
			if err != nil {
				return err
			}
			hash.Write(buffer[:n])
			if err := c.w.WriteCtx(ctx, proto.TUploadChunk, proto.EncodeChunk(id, buffer[:n])); err != nil {
				return err
			}
			offset += int64(n)
		}
		var extra [1]byte
		n, endErr := input.Read(extra[:])
		if n != 0 || !errors.Is(endErr, io.EOF) || hex.EncodeToString(hash.Sum(nil)) != file.hash {
			return copyInputError("copy_changed", "the local source changed during its checked upload")
		}
	}
	if err := o.check(ctx); err != nil {
		return err
	}
	if err := c.w.WriteJSONCtx(ctx, proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: hex.EncodeToString(hash.Sum(nil))}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case answer, ok := <-response:
		if !ok {
			return ErrClosed
		}
		if !answer.OK {
			if answer.Error != nil {
				return hostErrorOn(c.Host.Name, answer.Error)
			}
			return copyClientError("cp_path", "the sandbox did not accept the selected file", "Check the running sandbox and destination path.")
		}
		var result proto.UploadResult
		if !api.ExactJSONFields(answer.Result, result) || json.Unmarshal(answer.Result, &result) != nil || result.Path != file.target {
			return copyClientError("cp_path", "the sandbox changed the checked copy destination", "Update the helper and repeat the copy.")
		}
		failed = false
		return nil
	}
}

// Close cancels the dedicated transport and releases this original local plan.
func (o *CopyIn) Close() error {
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
	if o.root != nil {
		failures = append(failures, o.root.Close())
	}
	if o.operation != nil {
		failures = append(failures, o.operation.Close())
	}
	return errors.Join(failures...)
}
