package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// originalStartup retains the ONE accepted physical line. The native host
// owner issues the operation; this consumer cannot manufacture a replacement.
type originalStartup struct {
	mu sync.Mutex
	line *host.StartupLineV1
	values map[string]string
	readDone chan struct{}
	cancelRequest uint64
	ready bool
}

// awaitOriginalLoan runs after the holder lease and socket exist, before any
// runtime factory, agent pin, mount or guest is allowed to start.
func (s *Supervisor) awaitOriginalLoan(ctx context.Context) (*host.SessionOperation, error) {
	listener, ok := s.ln.(*net.UnixListener)
	if !ok || s.bootOwner == nil { return nil, execRefusal("the startup has no original native listener and supervisor owner") }
	deadline := time.Now().Add(secretsWait)
	_ = listener.SetDeadline(deadline)
	defer listener.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func(){ _ = listener.SetDeadline(time.Now()) })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil { return nil, wallError("the original outside startup loan did not arrive", err) }
		operation, line, err := s.readOriginalLoan(ctx, conn, deadline)
		if err == nil {
			s.originalStart = &originalStartup{line:line, readDone:make(chan struct{})}
			return operation,nil
		}
		_ = conn.Close()
		if ctx.Err()!=nil { return nil,ctx.Err() }
	}
}

func (s *Supervisor) readOriginalLoan(ctx context.Context, conn net.Conn, deadline time.Time) (*host.SessionOperation,*host.StartupLineV1,error) {
	line,err:=host.NewStartupLineV1(conn)
	if err!=nil{return nil,nil,err}
	retained:=false
	defer func(){if !retained {_=line.Close()}}()
	_ = line.SetDeadline(minTime(deadline,time.Now().Add(3*time.Second)))
	frame,err:=line.ReadFrame()
	if err!=nil{return nil,nil,err}
	if err=line.RejectAncillary();err!=nil{return nil,nil,err}
	var hello proto.Hello
	if frame.Type!=proto.THello || json.Unmarshal(frame.Payload,&hello)!=nil || hello.Check(version.Protocol)!=nil {return nil,nil,execRefusal("startup requires an exact compatible hello")}
	if !host.FullPeer(conn,s.id,s.peerCheck) {return nil,nil,host.ErrInside}
	nonce,err:=s.bootOwner.PrepareStartupHello(ctx,line)
	if err!=nil{return nil,nil,err}
	if err=line.WriteFrame(proto.THello,proto.Hello{Magic:proto.Magic,Protocol:version.Protocol,Version:version.Version,Role:"supervisor",Features:Features,StartupNonce:nonce});err!=nil{return nil,nil,err}
	frame,err=line.ReadFrame()
	if err!=nil{return nil,nil,err}
	operation,err:=s.bootOwner.AcceptStartupLoan(ctx,line,frame)
	if err!=nil{return nil,nil,err}
	var request proto.Request
	if json.Unmarshal(frame.Payload,&request)!=nil {operation.Close();return nil,nil,execRefusal("the accepted loan request lost its reply identity")}
	if err=s.bootOwner.ReplyStartupLoan(line,request.ID,operation);err!=nil {operation.Close();s.cancelOriginalStartup(err);return nil,nil,err}
	retained=true
	return operation,line,nil
}

// receiveOriginalStart always consumes the selected envelope on the SAME loan
// line, including a boot with no secret values or automatic allow-missing.
func (s *Supervisor) receiveOriginalStart(ctx context.Context) error {
	start:=s.originalStart
	if start==nil || start.line==nil {return execRefusal("startup has no accepted original loan line")}
	_ = start.line.SetDeadline(time.Now().Add(secretsWait))
	stop:=context.AfterFunc(ctx,func(){_ = start.line.SetDeadline(time.Now())})
	frame,err:=start.line.ReadFrame()
	stop()
	if err!=nil{return err}
	if err=s.bootOwner.CheckStartupFrame(start.line);err!=nil{return err}
	var request proto.Request
	if frame.Type!=proto.TRequest || !api.ExactJSONFields(frame.Payload,proto.Request{}) || json.Unmarshal(frame.Payload,&request)!=nil || request.ID==0 || request.Op!="secrets" {return execRefusal("the original loan needs its exact selected start envelope")}
	values,err:=s.validateStartupSecrets(ctx,request.Args,s.m.Secrets)
	request.Args=nil
	if err!=nil{_ = start.line.WriteFrame(proto.TResponse,proto.Response{ID:request.ID,Error:toProtoErr(err)});return err}
	if err=start.line.WriteFrame(proto.TResponse,proto.Response{ID:request.ID,OK:true});err!=nil{return err}
	start.values=values
	_ = start.line.SetDeadline(time.Time{})
	s.originalReaderStarted=true
	go s.readOriginalControl(start)
	return nil
}

// readOriginalControl keeps the sole reader active during boot. Ready only
// registers a reply; cancellation can still revoke and join the actual boot.
func (s *Supervisor) readOriginalControl(start *originalStartup) {
	defer close(start.readDone)
	for {
		frame,err:=start.line.ReadFrame()
		if err!=nil {
			ready:=s.bootOwner.StartupCommittedV1()
			if !ready {s.cancelOriginalStartup(err)}
			return
		}
		if err=start.line.RejectAncillary();err!=nil{s.cancelOriginalStartup(err);return}
		var request proto.Request
		if frame.Type!=proto.TRequest || !api.ExactJSONFields(frame.Payload,proto.Request{}) || json.Unmarshal(frame.Payload,&request)!=nil || request.ID==0 || len(request.Args)!=0 {s.cancelOriginalStartup(execRefusal("the original startup control request changed"));return}
		switch request.Op {
		case host.StartupReadyOperationV1:
			if err=s.bootOwner.RegisterStartupReady(start.line,request.ID);err!=nil{s.cancelOriginalStartup(err);return}
		case "cancel_start":
			ready:=s.bootOwner.StartupCommittedV1()
			if ready {_=start.line.WriteFrame(proto.TResponse,proto.Response{ID:request.ID,Error:toProtoErr(execRefusal("the original startup is already durably ready"))});return}
			if err=s.bootOwner.CheckStartupFrame(start.line);err!=nil{s.cancelOriginalStartup(err);return}
			start.mu.Lock();start.cancelRequest=request.ID;start.mu.Unlock()
			s.cancelOriginalStartup(errStopped)
			return // ACK waits for final resource/borrowed-OP/holder cleanup.
		default:
			s.cancelOriginalStartup(execRefusal("the original startup control operation is unsupported"));return
		}
	}
}

func (s *Supervisor) cancelOriginalStartup(err error) {
	if s.bootOwner!=nil && s.bootOwner.StartupCommittedV1(){return}
	s.markStopIntent()
	if s.cancelLifetime!=nil{s.cancelLifetime(err)}
	if s.ln!=nil{_ = s.ln.Close()}
}

func (s *Supervisor) commitOriginalReady() error {
	if err:=s.reconcilePorts();err!=nil{return err}
	start:=s.originalStart
	if start==nil{return nil} // The explicit plain test driver has no outside issuer.
	err:=s.bootOwner.CommitStartupReady(s.lifetimeContext(),s.bootOperation)
	if !s.bootOwner.StartupCommittedV1(){if err==nil{return execRefusal("the original native startup did not commit readiness")};return err}
	start.mu.Lock();start.ready=true;start.mu.Unlock()
	if err!=nil{s.log.Warn("the committed original startup could not acknowledge its outside parent","err",err)}
	return nil
}

// finishOriginalStartup runs after actual runtime cleanup. The borrowed OP and
// holder lease close before any cancellation ACK; physical reader join is real.
func (s *Supervisor) finishOriginalStartup(cleanup error) error {
	if s.bootOperation!=nil{cleanup=errors.Join(cleanup,s.bootOperation.CloseWithError())}
	if s.bootOwner!=nil{cleanup=errors.Join(cleanup,s.bootOwner.CloseWithError())}
	start:=s.originalStart
	if start==nil{return cleanup}
	start.mu.Lock();request:=start.cancelRequest;start.mu.Unlock()
	if request!=0 && cleanup==nil {_=start.line.WriteFrame(proto.TResponse,proto.Response{ID:request,OK:true})}
	cleanup=errors.Join(cleanup,start.line.Close())
	select {case <-start.readDone:default:
		// receiveOriginalStart starts this reader only after the start ACK.
		if s.originalReaderStarted {<-start.readDone}
	}
	return cleanup
}
