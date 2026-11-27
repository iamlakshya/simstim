package image

import (
	"io/fs"
	"os"
)

type agentsNode struct {
	name, relative, link string
	root                 *os.Root
	file                 *os.File
	info                 fs.FileInfo
	virtual              *agentsEntry
}

type agentsQueue []agentsNode

func (q agentsQueue) Len() int           { return len(q) }
func (q agentsQueue) Less(i, j int) bool { return q[i].name < q[j].name }
func (q agentsQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *agentsQueue) Push(value any)    { *q = append(*q, value.(agentsNode)) }
func (q *agentsQueue) Pop() any {
	last := len(*q) - 1
	value := (*q)[last]
	(*q)[last] = agentsNode{}
	*q = (*q)[:last]
	return value
}
