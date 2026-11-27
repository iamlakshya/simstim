package image

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ConvertAgentsV1 is the raw-tar converter operation. The normal producer
// ALWAYS invokes it inside the canonical no-network converter jail and owned
// memory scope. This function has no path input or unjailed fallback.
func ConvertAgentsV1(ctx context.Context, input io.Reader, output *os.File) error {
	if input == nil || output == nil {
		return agentsDiskError("the jailed converter has missing IO")
	}
	if err := output.Truncate(0); err != nil {
		return err
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { err := normaliseAgentsTar(ctx, input, writer); _ = writer.CloseWithError(err); done <- err }()
	view := &virtualFile{file: output, limit: MaxAgentsDisk, failure: func(string) error { return agentsDiskError("the complete filesystem exceeds its 4 GiB disk cap") }}
	conversion := tar2ext4.Convert(reader, view, tar2ext4.MaximumDiskSize(MaxAgentsDisk))
	_ = reader.CloseWithError(conversion)
	source := <-done
	if err := ctx.Err(); err != nil {
		return err
	}
	var typed *proto.Error
	if errors.As(conversion, &typed) && typed.Code == "agents_disk" {
		return conversion
	}
	if source != nil {
		return source
	}
	if conversion != nil {
		return agentsDiskError("tar2ext4 could not convert the installed agents: " + conversion.Error())
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if view.length <= 0 || view.length > MaxAgentsDisk || view.length%4096 != 0 {
		return agentsDiskError("the converter returned an unaligned or oversized disk")
	}
	if err := output.Truncate(view.length); err != nil {
		return err
	}
	return output.Sync()
}

func normaliseAgentsTar(ctx context.Context, input io.Reader, output io.Writer) error {
	reader := tar.NewReader(&flatContextReader{ctx: ctx, r: input})
	writer := tar.NewWriter(output)
	entries := 0
	previous := ""
	directories := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return agentsDiskError("the installed agents tar is malformed")
		}
		entries++
		if entries > MaxAgentsEntries {
			return agentsDiskError("the installed agents contain more than 500000 entries")
		}
		name := header.Name
		if !path.IsAbs(name) && path.Clean(name) == name && (name == "bin" || name == "root" || strings.HasPrefix(name, "bin/") || strings.HasPrefix(name, "root/")) {
			if previous != "" && name <= previous {
				return agentsDiskError("the installed tar is not in unique byte order")
			}
		} else {
			return agentsDiskError("the installed tar contains an invalid disk path")
		}
		previous = name
		if name == "bin" || name == "root" {
			if header.Typeflag != tar.TypeDir {
				return agentsDiskError("the canonical root/bin layout is not a directory")
			}
		} else if !directories[path.Dir(name)] {
			return agentsDiskError("an installed tar child precedes or replaces its directory")
		}
		if strings.HasPrefix(name, "bin/") && (strings.Contains(strings.TrimPrefix(name, "bin/"), "/") || header.Typeflag != tar.TypeSymlink || !strings.HasPrefix(header.Linkname, "../root/") || !agentsHostPath("/"+strings.TrimPrefix(header.Linkname, "../root/"))) {
			return agentsDiskError("an installed bin entry is not the resolved binary symlink")
		}
		if header.Uid != 1000 || header.Gid != 1000 {
			return agentsDiskError("the installed tar does not use the fixed agent owner")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeSymlink {
			return agentsDiskError("the installed tar contains an unsupported entry type")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Size != 0 {
			return agentsDiskError("a non-regular installed tar entry has a body")
		}
		if header.Typeflag == tar.TypeDir {
			directories[name] = true
		}
		if err := flatMetadata(header); err != nil {
			return agentsDiskError(err.Error())
		}
		header.Format = tar.FormatPAX
		header.PAXRecords = nil
		header.Xattrs = nil
		header.Uname, header.Gname = "", ""
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err := io.CopyN(writer, reader, header.Size); err != nil {
			return err
		}
	}
	if !directories["bin"] || !directories["root"] {
		return agentsDiskError("the installed tar omits its canonical root/bin layout")
	}
	return writer.Close()
}

// InspectAgentsV1 validates complete ext4 layout without a verity trailer.
// The store separately validates descriptor ownership/mode and hashes bytes.
func InspectAgentsV1(file *os.File) (int64, error) {
	if file == nil {
		return 0, agentsDiskError("the agents output descriptor is missing")
	}
	size, block, err := tar2ext4.Ext4FileSystemSize(io.NewSectionReader(file, 0, MaxAgentsDisk))
	if err != nil {
		return 0, fmt.Errorf("inspect agents ext4: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if size <= 0 || size > MaxAgentsDisk || block != 4096 || info.Size() != size || size%4096 != 0 {
		return 0, agentsDiskError("the agents disk layout, length or block alignment is invalid")
	}
	return size, nil
}
