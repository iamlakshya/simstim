package link

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestExecContinuationRetainsAlreadyBufferedFramesWithoutAnotherHello(t *testing.T) {
	var preface, records bytes.Buffer
	w := proto.NewWriter(&preface)
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: 1, OK: true})
	open := binary.LittleEndian.AppendUint32(nil, 1)
	open = append(open, []byte(`{"kind":"exec","id":"a2a2a2a2"}`)...)
	_ = w.Write(proto.TStreamOpen, open)
	output := NewExecRecords(&records)
	_, _ = output.Output(ExecStdout, []byte("\x00\xff\x1b[A\r\n"))
	_ = output.End(127)
	data := binary.LittleEndian.AppendUint32(nil, 1)
	data = append(data, records.Bytes()...)
	_ = w.Write(proto.TStreamData, data)
	closeWrite := binary.LittleEndian.AppendUint32(nil, 1)
	closeWrite = append(closeWrite, 1)
	_ = w.Write(proto.TStreamClose, closeWrite)
	ours, theirs := net.Pipe()
	defer theirs.Close()
	drain := make(chan struct{})
	go func() { defer close(drain); _, _ = io.Copy(io.Discard, theirs) }()
	reader := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(preface.Bytes()), ours), 64<<10)
	frame, err := proto.Read(reader)
	if err != nil || frame.Type != proto.TResponse {
		t.Fatal(frame, err)
	}
	if reader.Buffered() == 0 {
		t.Fatal("fixture did not buffer stream bytes after response")
	}
	session := ContinueExec(ours, reader, Guest)
	defer func() { _ = session.Close(); <-drain }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := session.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	var got bytes.Buffer
	code, err := ReadExecOutput(stream, &got, nil)
	if err != nil || code != 127 || !bytes.Equal(got.Bytes(), []byte("\x00\xff\x1b[A\r\n")) {
		t.Fatalf("code%d err%v data%q", code, err, got.Bytes())
	}
}
