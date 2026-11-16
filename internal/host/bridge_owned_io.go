package host

import "errors"

// RunBridgeOwnedIO transfers the one physical reader and writer, including
// their real cancellation and join rights. Legacy borrowed bridge callers do
// not acquire custom-stream capabilities merely by passing io.Closer values.
func RunBridgeOwnedIO(in OwnedBridgeInput,out OwnedBridgeOutput,args ...string)(returned error){
 if in==nil || out==nil{return UserError("bridge_io","the bridge has no complete owned input and output","Use the complete host bridge entrypoint.")}
 defer func(){
  // Interrupt both directions before joining either actual resource.
  cancelIn,cancelOut:=in.Cancel(),out.Cancel()
  returned=errors.Join(returned,cancelIn,cancelOut,in.Wait(),out.Wait())
 }()
 return runBridge(BridgeInput(in,args),out,out,in)
}
