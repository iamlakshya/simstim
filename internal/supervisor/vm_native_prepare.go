package supervisor

import (
	"context"
	"net"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// retainVMStart consumes the exact Start result before any VMPrepare. The
// opaque engine binding supplies native facts; the original receipt supplies
// the actual operation, protected Need and once-only prepare permission.
func (s *Supervisor) retainVMStart(ctx context.Context,runtime sandbox,connection net.Conn) error {
	if s.m.Wall==nil || s.m.Wall.Driver!="chv" && s.m.Wall.Driver!="firecracker" {return nil}
	if _,err:=s.protectedVMDriver();err!=nil{return err}
	producer,ok:=runtime.(interface{NativeBindingV1(context.Context,net.Conn)(*microvm.NativeBindingV1,error)})
	if !ok{return execRefusal("the selected microvm has no actual engine-owned native binding")}
	binding,err:=producer.NativeBindingV1(ctx,connection)
	if err!=nil{return err}
	receipt:=host.OriginalVMNeedFromContext(s.lifetimeContext())
	if receipt==nil{return execRefusal("the original microvm operation ended before binding its native connection")}
	correlation,err:=receipt.PrepareCorrelation(ctx,binding,connection)
	if err!=nil{return err}
	s.vmStartConnection,s.vmCorrelation=connection,correlation
	return nil
}

// NativeBindingV1 preserves the concrete engine's optional native contract
// through the ordinary sandbox adapter; it issues no replacement binding.
func (r vmRuntime) NativeBindingV1(ctx context.Context,connection net.Conn)(*microvm.NativeBindingV1,error) {
	producer,ok:=r.Sandbox.(interface{NativeBindingV1(context.Context,net.Conn)(*microvm.NativeBindingV1,error)})
	if !ok{return nil,execRefusal("the actual microvm engine supplies no native connection binding")}
	return producer.NativeBindingV1(ctx,connection)
}

func (s *Supervisor) confirmVMPrepare(ctx context.Context,echo *proto.VMPrepareCorrelationV1)error {
	receipt:=host.OriginalVMNeedFromContext(s.lifetimeContext())
	if receipt==nil || s.vmStartConnection==nil{return execRefusal("VM prepare has no original same-connection receipt")}
	return receipt.ConfirmPreparedCorrelation(ctx,s.vmStartConnection,echo)
}
