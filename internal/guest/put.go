package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// PutHelperMode is the closed, fixed-account file receiver entrypoint.
const PutHelperMode = "wall-put-v1"

func putError(message string) *proto.Error {
	return &proto.Error{Code: "upload", Message: message, Hint: "Check the file and the running sandbox, then upload it again."}
}

func putTarget(header proto.FilePutHeaderV1) (top, relative string, err error) {
	if header.Version != proto.FileTransferVersionV1 || header.Size < 0 || len(header.SHA256) != 64 {
		return "", "", putError("invalid file transfer declaration")
	}
	for _, char := range header.SHA256 {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return "", "", putError("invalid file transfer digest")
			}
		}
	}
	if err := filecopy.Name(header.Name); err != nil {
		return "", "", err
	}
	if header.Directory && (header.Path=="" || header.Size!=0 || header.Replace || header.SHA256!=api.EmptyInputHash) { return "","",putError("invalid empty-directory declaration") }
	if header.Path != "" {
		return filecopy.GuestPath(header.Path)
	}
	if header.Replace || header.Size > host.MaxUpload {
		return "", "", putError("invalid paste declaration")
	}
	return path.Join(host.GuestRunDir(), "paste"), header.SHA256[:12] + "-" + header.Name, nil
}

// RunPutHelper receives only one bounded declaration followed by exact bytes.
// It is started by Init through the same fixed UID1000 owned command runner.
func RunPutHelper(args []string) (returned error) {
	if len(args) != 0 {
		return putError("the file receiver takes no arguments")
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := proto.ReadJSONLine(reader)
	if err != nil || len(line) > link.DefaultMaxHeader {
		return putError("invalid file receiver metadata")
	}
	var header proto.FilePutHeaderV1
	if !api.ExactJSONFields(line, header) || json.Unmarshal(line, &header) != nil {
		return putError("invalid file receiver fields")
	}
	top, relative, err := putTarget(header)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	result := proto.FilePutResultV1{Version: proto.FileTransferVersionV1, Path: path.Join(top, relative)}
	var receiveErr error
	if header.Directory { receiveErr=filecopy.MakeDirectory(context.Background(),root,relative,reader) } else { receiveErr=filecopy.ReceiveFile(context.Background(),root,relative,header.Size,header.SHA256,header.Replace,reader) }
	if receiveErr!=nil { result.Path,result.Error="",asPutError(receiveErr) }
	return json.NewEncoder(os.Stdout).Encode(result)
}

// acceptPut admits its lifecycle under the same mutex that begins guest stop.
func (g *Init) acceptPut(st *link.Stream) {
	g.mu.Lock()
	if g.ended || !g.sessionPrepared || g.lifetime == nil {
		g.mu.Unlock()
		_ = st.Close()
		return
	}
	g.fileWG.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.fileWG.Done()
		g.servePut(st)
	}()
}

func (g *Init) servePut(st *link.Stream) {
	defer st.Close()
	ctx, cancel := context.WithTimeout(g.lifetime, 2*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = st.SetDeadline(time.Now()); _ = st.Close() })
	defer stop()
	var header proto.FilePutHeaderV1
	if st.Header(&header) != nil || !api.ExactJSONFields(st.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		proto.FilePutHeaderV1
	}{}) {
		return
	}
	top, relative, err := putTarget(header)
	if err != nil {
		g.putReply(st, "", err)
		return
	}
	if header.Path == "" {
		if err := g.preparePasteDirectory(); err != nil {
			g.putReply(st, "", err)
			return
		}
	}
	executable, err := g.helperExecutable()
	if err != nil {
		g.putReply(st, "", err)
		return
	}
	prefix, err := json.Marshal(header)
	if err != nil {
		g.putReply(st, "", err)
		return
	}
	run := newOwnedCommand(ctx, proto.CtlExecArgs{Argv: []string{executable, PutHelperMode}, Cwd: "/"}, inputCommandEnv())
	run.vmRole = vmSeedCommand
	defer run.cancel()
	var output putOutput
	code := g.executeOwned(run, ownedCommandIO{
		Out: &output, Err: io.Discard,
		AbortOutput: func() { _ = st.SetDeadline(time.Now()) },
		AttachInput: func(input io.WriteCloser) func() {
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer input.Close()
				_, _ = io.Copy(input, io.MultiReader(bytes.NewReader(append(prefix, '\n')), st))
			}()
			return func() { _ = st.SetReadDeadline(time.Now()); <-done }
		},
	})
	var result proto.FilePutResultV1
	if code == 0 && ctx.Err() == nil && json.Unmarshal(output.Bytes(), &result) == nil && result.Version == proto.FileTransferVersionV1 && result.Error != nil && result.Path == "" {
		g.putReply(st, "", result.Error)
		return
	}
	if code != 0 || ctx.Err() != nil || json.Unmarshal(output.Bytes(), &result) != nil || result.Version != proto.FileTransferVersionV1 || result.Error != nil || result.Path != path.Join(top, relative) {
		g.putReply(st, "", putError("the fixed-account file receiver did not finish successfully"))
		return
	}
	g.putReply(st, result.Path, nil)
}

func (g *Init) putReply(st *link.Stream, target string, err error) {
	result := proto.FilePutResultV1{Version: proto.FileTransferVersionV1, Path: target}
	if err != nil {
		result.Error = asPutError(err)
	}
	_ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait))
	_ = json.NewEncoder(st).Encode(result)
	_ = st.CloseWrite()
}

type putOutput struct{ bytes.Buffer }

func (w *putOutput) Write(data []byte) (int, error) {
	if len(data) > link.DefaultMaxHeader-w.Len() {
		return 0, putError("the file receiver sent oversized metadata")
	}
	return w.Buffer.Write(data)
}

// preparePasteDirectory creates only the fixed, account-owned destination.
// Init never opens an uploaded file or a caller-selected guest path as root.
func (g *Init) preparePasteDirectory() (returned error) {
	root, err := os.OpenRoot(g.path(host.GuestRunDir()))
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	if err := root.Chmod(".", 0o711); err != nil {
		return err
	}
	if err := root.Mkdir("paste", 0o700); err == nil {
		if err := root.Chown("paste", AgentUID, AgentGID); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat("paste")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !inputOwner(info, AgentUID) || info.Mode().Perm() != 0o700 {
		return putError("the paste folder is not the fixed account's ordinary private folder")
	}
	return nil
}

func asPutError(err error) *proto.Error {
	var refusal *proto.Error
	if errors.As(err, &refusal) {
		return refusal
	}
	var file *filecopy.Error
	if errors.As(err, &file) {
		return &proto.Error{Code: file.Code, Message: file.Message, Hint: file.Hint}
	}
	return putError("the guest could not receive the file")
}
