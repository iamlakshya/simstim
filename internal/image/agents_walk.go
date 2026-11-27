package image

import (
	"container/heap"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/files"
)

type agentsVisitor func(agentsEntry, *os.File) error

// A whole-name queue is necessary: a directory a must precede its children,
// but a sibling a- precedes a/child. Depth-first traversal cannot provide the
// specified byte order. Candidates retain metadata and rooted paths, never
// one open descriptor per queued directory or a body-sized buffer.
func walkAgentsPlan(ctx context.Context, inputs []AgentsInputV1, virtual []agentsEntry, visit agentsVisitor) error {
	queue := &agentsQueue{}
	for _, entry := range virtual {
		heap.Push(queue, agentsNode{name: entry.name, virtual: &entry})
	}
	for _, source := range inputs {
		node := agentsNode{name: "root/" + strings.TrimPrefix(source.HostPath, "/"), root: source.Folder, file: source.File, link: source.LinkTarget, relative: "."}
		var err error
		switch {
		case source.LinkInfo != nil:
			node.info = source.LinkInfo
		case source.File != nil:
			node.info, err = source.File.Stat()
		default:
			node.info, err = source.Folder.Stat(".")
		}
		if err != nil {
			return err
		}
		heap.Push(queue, node)
	}
	visited := 0
	for queue.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		node := heap.Pop(queue).(agentsNode)
		visited++
		if visited > MaxAgentsEntries {
			return agentsDiskError("the installed agents contain more than 500000 entries")
		}
		if err := visitAgentsNode(ctx, node, queue, visited, visit); err != nil {
			return err
		}
	}
	return nil
}

func visitAgentsNode(ctx context.Context, node agentsNode, queue *agentsQueue, visited int, visit agentsVisitor) error {
	if node.virtual != nil {
		return visit(*node.virtual, nil)
	}
	if node.root == nil {
		if node.file != nil {
			current, err := node.file.Stat()
			if err != nil || !current.Mode().IsRegular() || !sameAgentsInfo(node.info, current) {
				return agentsDiskError("an installed binary changed while queued")
			}
		} else if node.info.Mode()&fs.ModeSymlink == 0 {
			return agentsDiskError("an installed link source is not a symlink")
		}
		entry, err := agentsHeader(node.name, node.info, node.link)
		if err != nil {
			return err
		}
		return visit(entry, node.file)
	}
	current, err := node.root.Lstat(node.relative)
	if err != nil || !sameAgentsInfo(node.info, current) {
		return agentsDiskError("an installed entry changed while queued")
	}
	if current.Mode()&fs.ModeSymlink != 0 {
		target, err := node.root.Readlink(node.relative)
		if err != nil {
			return err
		}
		after, err := node.root.Lstat(node.relative)
		if err != nil || !sameAgentsInfo(current, after) {
			return agentsDiskError("an installed link changed during its snapshot")
		}
		entry, err := agentsHeader(node.name, current, target)
		if err != nil {
			return err
		}
		return visit(entry, nil)
	}
	if current.IsDir() {
		child, err := node.root.OpenRoot(node.relative)
		if err != nil {
			return err
		}
		defer child.Close()
		opened, err := child.Stat(".")
		if err != nil || !sameAgentsInfo(current, opened) {
			return agentsDiskError("an installed directory changed during its snapshot")
		}
		entry, err := agentsHeader(node.name, opened, "")
		if err != nil {
			return err
		}
		if err := visit(entry, nil); err != nil {
			return err
		}
		return queueAgentsChildren(ctx, node, child, queue, visited)
	}
	input, err := files.OpenRegular(node.root, node.relative)
	if err != nil {
		return agentsDiskError("an installed source is not a safe regular file")
	}
	opened, err := input.Stat()
	if err != nil || !sameAgentsInfo(current, opened) {
		input.Close()
		return agentsDiskError("an installed file changed during its snapshot")
	}
	entry, err := agentsHeader(node.name, opened, "")
	if err == nil {
		err = visit(entry, input)
	}
	return errors.Join(err, input.Close())
}

func queueAgentsChildren(ctx context.Context, parent agentsNode, root *os.Root, queue *agentsQueue, visited int) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := directory.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(children) > MaxAgentsEntries-visited-queue.Len() {
			return agentsDiskError("the installed agents contain more than 500000 entries")
		}
		for _, child := range children {
			info, err := root.Lstat(child.Name())
			if err != nil {
				return err
			}
			heap.Push(queue, agentsNode{name: path.Join(parent.name, child.Name()), root: parent.root, relative: path.Join(parent.relative, child.Name()), info: info})
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func sameAgentsInfo(a, b fs.FileInfo) bool {
	if a == nil || b == nil || !os.SameFile(a, b) || a.Size() != b.Size() || a.Mode() != b.Mode() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	first, err := agentsFileIdentity(a)
	if err != nil {
		return false
	}
	second, err := agentsFileIdentity(b)
	return err == nil && first == second
}
