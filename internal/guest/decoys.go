package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// DecoyHelperMode is the closed, fixed-account home-file helper entrypoint.
const DecoyHelperMode = "wall-decoys-v1"

// RunDecoyHelper installs one complete private start generation as UID1000.
// It accepts no caller path, command, account, mode or environment argument.
func RunDecoyHelper(args []string) (returned error) {
	if len(args) != 0 {
		return decoyFailure("the decoy file helper takes no arguments")
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, DecoyPayloadMax+1))
	if err != nil || len(data) == 0 || len(data) > DecoyPayloadMax {
		return decoyFailure("the decoy file payload is missing or exceeds its bound")
	}
	files, err := decodeDecoyFiles(data)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(host.GuestHome)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	return writeDecoys(context.Background(), root, files)
}

func decodeDecoyFiles(data []byte) (*proto.DecoyFilesV1, error) {
	var files proto.DecoyFilesV1
	if len(data) == 0 || len(data) > DecoyPayloadMax || !api.ExactJSONFields(data, files) || json.Unmarshal(data, &files) != nil {
		return nil, decoyFailure("the decoy file payload is malformed")
	}
	if err := ValidateDecoyFilesV1(&files); err != nil {
		return nil, err
	}
	return &files, nil
}

func decoyParent(root *os.Root, relative string, create bool) (_ *os.Root, returned error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, current.Close())
		}
	}()
	parent := path.Dir(relative)
	if parent == "." {
		return current, nil
	}
	for _, component := range strings.Split(parent, "/") {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = current.Mkdir(component, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			info, err = current.Lstat(component)
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, decoyFailure("a decoy parent is a link or is not a directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return nil, errors.Join(decoyFailure("a decoy parent changed while opening"), err, next.Close())
		}
		if err := current.Close(); err != nil {
			return nil, errors.Join(err, next.Close())
		}
		current = next
	}
	return current, nil
}

func preflightDecoy(root *os.Root, relative string) (returned error) {
	parent, err := decoyParent(root, relative, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, parent.Close()) }()
	_, err = parent.Lstat(path.Base(relative))
	if !errors.Is(err, os.ErrNotExist) {
		return decoyFailure("a decoy destination already exists or cannot be checked")
	}
	return nil
}

func writeDecoys(ctx context.Context, root *os.Root, input *proto.DecoyFilesV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateDecoyFilesV1(input); err != nil {
		return err
	}
	if input == nil {
		return nil
	}
	if root == nil {
		return decoyFailure("the decoy home is unavailable")
	}
	definitions, err := DecoyDefinitions()
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if err := preflightDecoy(root, definition.Path); err != nil {
			return err
		}
	}
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			return err
		}
		var content string
		for _, file := range input.Files {
			if file.Name == definition.Name {
				content = file.Content
			}
		}
		if err := writeDecoy(root, definition.Path, content); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func writeDecoy(root *os.Root, relative, content string) (returned error) {
	parent, err := decoyParent(root, relative, true)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, parent.Close()) }()
	file, err := parent.OpenFile(path.Base(relative), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if count, err := io.WriteString(file, content); err != nil || count != len(content) {
		return errors.Join(io.ErrShortWrite, err)
	}
	// There is no transient public filename to reclaim or sweep. On a failed
	// boot, the runtime retains and resets its actual fresh home; this helper
	// never unlinks a name another UID1000 process could have replaced.
	return file.Sync()
}
