package client

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// readEnvironmentFile grants the explicitly chosen containing directory.
// The leaf may be an ordinary contained alias. Root prevents an escape or
// magic link and the descriptor remains pinned throughout the bounded read.
func readEnvironmentFile(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := files.OpenContainedRegular(root, filepath.Base(absolute))
	if err != nil {
		return nil, envInputError("bad_env_file", "the environment file is not an ordinary file inside its chosen folder", "Choose a readable local file without an escaping link.", KindUsage)
	}
	defer file.Close()
	data, err := io.ReadAll(&inputContextReader{ctx: ctx, reader: io.LimitReader(file, proto.MaxFrame+1)})
	if err != nil {
		return nil, err
	}
	if len(data) > proto.MaxFrame {
		return nil, envInputError("env_too_big", "the environment file exceeds the creation frame bound", "Use a smaller environment file.", KindUsage)
	}
	return data, ctx.Err()
}
