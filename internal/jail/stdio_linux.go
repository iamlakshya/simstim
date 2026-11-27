package jail

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// streams names the jail's stdin, stdout and stderr, in fd order.
var streams = [3]string{"Stdin", "Stdout", "Stderr"}

// StdinPipe returns a pipe to the command's stdin: the write end of an
// os.Pipe. Wait closes it after the jail ends, and so does a Start that
// fails.
func (c *Cmd) StdinPipe() (io.WriteCloser, error) {
	f, err := c.pipe(0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// StdoutPipe returns a pipe from the command's stdout: the read end of an
// os.Pipe. Wait closes it after the jail ends, so read it all before Wait,
// as with exec.Cmd. A Start that fails closes it too.
func (c *Cmd) StdoutPipe() (io.ReadCloser, error) {
	f, err := c.pipe(1)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// StderrPipe returns a pipe from the command's stderr, as StdoutPipe does.
func (c *Cmd) StderrPipe() (io.ReadCloser, error) {
	f, err := c.pipe(2)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// pipe makes the pipe of stream n, keeps the jail's end, and returns the
// caller's end. The state check and stream reservation are one operation.
func (c *Cmd) pipe(n int) (*os.File, error) {
	name := streams[n]
	c.mu.Lock()
	defer c.mu.Unlock()
	set := hasStream([3]any{c.Stdin, c.Stdout, c.Stderr}[n])
	switch {
	case c.phase != phaseNew:
		return nil, failure("the jail's "+name+"Pipe comes after Start", "Call "+name+"Pipe before Start.")
	case set || c.pipes[n] != nil:
		return nil, failure("the jail's "+name+" is set twice", "Set "+name+", or use "+name+"Pipe, not both.")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, failure("cannot make the jail's pipe: "+err.Error(), "Check the open-file limit.")
	}
	child, parent := w, r
	if n == 0 {
		child, parent = r, w
	}
	c.pipes[n] = child
	c.ends = append(c.ends, parent)
	return parent, nil
}

// closeEnds closes the jail's ends of the pipes, which it no longer needs
// once it started, and with all the caller's ends too.
func (c *Cmd) closeEnds(all bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, f := range c.pipes {
		if f != nil {
			_ = f.Close()
			c.pipes[i] = nil
		}
	}
	if all {
		for _, f := range c.ends {
			_ = f.Close()
		}
		c.ends = nil
	}
}

// stdio gives the jail its stdin, stdout and stderr. Each must be a file,
// a pipe from StdinPipe, StdoutPipe or StderrPipe, or nil: the jail copies
// no stdio, so Wait never waits on a copy, and no copy can fail unseen.
// It owns the captures passed to exec, so a caller closing an original
// cannot leave a child descriptor slot empty for internal state to reuse.
func (c *Cmd) stdio() ([]*os.File, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var opened []*os.File
	release := func() { closeFiles(opened) }
	fail := func(err error) ([]*os.File, func(), error) {
		release()
		return nil, nil, err
	}
	sets := [3]func(*os.File){
		func(f *os.File) { c.cmd.Stdin = f },
		func(f *os.File) { c.cmd.Stdout = f },
		func(f *os.File) { c.cmd.Stderr = f },
	}
	for n, v := range [3]any{c.Stdin, c.Stdout, c.Stderr} {
		name := streams[n]
		var source *os.File
		switch f := v.(type) {
		case nil:
		case *os.File:
			if f == nil {
				break // as a nil Reader or Writer
			}
			if c.pipes[n] != nil {
				return fail(failure("the jail's "+name+" is set twice", "Set "+name+", or use "+name+"Pipe, not both."))
			}
			source = f
		default:
			return fail(failure("the jail's "+name+" is a "+typeName(f)+", not a file: a jail copies no stdio",
				"Set "+name+" to an *os.File, or use "+name+"Pipe, which gives you the other end of a pipe."))
		}
		if c.pipes[n] != nil {
			source = c.pipes[n]
		}
		if source != nil {
			file, err := captureFile(source, name, "Give "+name+" an open file, or use "+name+"Pipe.")
			if err != nil {
				return fail(err)
			}
			opened = append(opened, file)
			if c.pipes[n] != nil {
				if err := blockingPipe(file, name); err != nil {
					return fail(err)
				}
			}
			sets[n](file)
		}
	}
	files := make([]*os.File, 0, len(c.files))
	for _, source := range c.files {
		file, err := captureFile(source, "inherited file", "Give Spec.Files only open files.")
		if err != nil {
			return fail(err)
		}
		opened = append(opened, file)
		files = append(files, file)
	}
	return files, release, nil
}

func typeName(v any) string { return strings.TrimPrefix(fmt.Sprintf("%T", v), "*") }

func hasStream(v any) bool {
	f, file := v.(*os.File)
	return v != nil && (!file || f != nil)
}

// samePaths refuses a pinned bind whose object no longer has the path that
// Command resolved. Such an object moved: for example, a read-only bind's
// folder moved into a writable bind, with a link at its old name.
func (c *Cmd) samePaths() error {
	for i, b := range c.binds {
		if err := samePath(int(c.pins[i].Fd()), b.Real); err != nil {
			return failure("the bound path "+quote(b.Path)+" changed: "+err.Error(),
				"Do not change a jail's bound paths between jail.Command and Start.")
		}
	}
	for _, r := range pinnedRules(c.rules) {
		i := r.FD - 5 - len(c.files) // binds/rules follow the files, set-up and status
		if i < 0 || i >= len(c.pins) {
			return failure("the jail's pinned rule descriptor is invalid", "Report this as a bug.")
		}
		if err := samePath(int(c.pins[i].Fd()), r.Real); err != nil {
			return failure("the Landlock rule's path "+quote(r.Path)+" changed: "+err.Error(),
				"Do not change a jail's paths between jail.Command and Start.")
		}
	}
	return nil
}
