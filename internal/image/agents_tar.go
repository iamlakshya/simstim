package image

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"strings"
)

// AgentsSourceDigestV1 fingerprints metadata using the SAME ordered walk as
// tar production. It reads no file bodies and creates no disk/provenance receipt.
func AgentsSourceDigestV1(ctx context.Context, inputs []AgentsInputV1, binaries []AgentBinaryV1) (string, error) {
	return agentsTar(ctx, inputs, binaries, nil)
}

// WriteAgentsTarV1 streams one deterministic tar. Its caller runs the complete
// walk inside the owning memory scope and pipes this to the converter jail.
// Data uses ReaderAt, preserving caller descriptor offsets and status flags.
func WriteAgentsTarV1(ctx context.Context, inputs []AgentsInputV1, binaries []AgentBinaryV1, output io.Writer) (string, error) {
	if output == nil {
		return "", agentsDiskError("the agents tar output is missing")
	}
	return agentsTar(ctx, inputs, binaries, output)
}

func agentsTar(ctx context.Context, inputs []AgentsInputV1, binaries []AgentBinaryV1, output io.Writer) (string, error) {
	plan, err := agentsInputPlan(inputs)
	if err != nil {
		return "", err
	}
	links, err := agentsBinaryPlan(binaries, plan)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("agents-source-v1\x00"))
	var writer *tar.Writer
	if output != nil {
		writer = tar.NewWriter(output)
	}
	entries := 0
	visit := func(entry agentsEntry, file *os.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > MaxAgentsEntries {
			return agentsDiskError("the installed agents contain more than 500000 entries")
		}
		hashAgentsEntry(digest, entry)
		if writer == nil {
			return nil
		}
		if err := writer.WriteHeader(entry.header); err != nil {
			return agentsDiskError("installed metadata cannot be encoded without alteration")
		}
		if entry.header.Typeflag == tar.TypeReg || entry.header.Typeflag == tar.TypeRegA {
			if file == nil {
				return agentsDiskError("an installed regular entry has no captured descriptor")
			}
			before, err := file.Stat()
			if err != nil {
				return err
			}
			identity, identityErr := agentsFileIdentity(before)
			if identityErr != nil || identity != entry.identity || before.Mode() != entry.sourceMode || before.Size() != entry.header.Size || !before.ModTime().Equal(entry.header.ModTime) {
				return agentsDiskError("an installed file changed before its data read")
			}
			if _, err := io.CopyN(writer, &flatContextReader{ctx: ctx, r: io.NewSectionReader(file, 0, entry.header.Size)}, entry.header.Size); err != nil {
				return err
			}
			after, err := file.Stat()
			if err != nil || !sameAgentsInfo(before, after) {
				return agentsDiskError("an installed file changed while its data was read")
			}
		}
		return nil
	}
	virtual := []agentsEntry{agentsSynthetic("bin", tar.TypeDir, ""), agentsSynthetic("root", tar.TypeDir, "")}
	for _, binary := range links {
		virtual = append(virtual, agentsSynthetic("bin/"+binary.Name, tar.TypeSymlink, "../root/"+strings.TrimPrefix(binary.ResolvedPath, "/")))
	}
	parents := map[string]bool{"root": true}
	for _, source := range plan {
		parent := "root"
		for _, component := range strings.Split(strings.TrimPrefix(path.Dir(source.HostPath), "/"), "/") {
			if component == "" || component == "." {
				continue
			}
			parent = path.Join(parent, component)
			if !parents[parent] {
				virtual = append(virtual, agentsSynthetic(parent, tar.TypeDir, ""))
				parents[parent] = true
			}
		}
	}
	if err := walkAgentsPlan(ctx, plan, virtual, visit); err != nil {
		return "", err
	}
	if writer != nil {
		if err := writer.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
