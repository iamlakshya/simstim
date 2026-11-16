//go:build linux || darwin

package main

import (
 "errors"
 "os"
 "github.com/iamlakshya/simstim/internal/host"
)

func init(){
 commands["bridge"]=runOwnedBridge
 commands["bridge-input"]=func(args []string)error{
  if len(args)!=0{return host.UserError("bad_request","the input helper takes no arguments","Start it only through the canonical bridge entrypoint.")}
  return host.RunProcessBridgeInput()
 }
 commands["bridge-output"]=func(args []string)error{
  if len(args)!=0{return host.UserError("bad_request","the output helper takes no arguments","Start it only through the canonical bridge entrypoint.")}
  return host.RunProcessBridgeOutput(os.Stdout)
 }
}

func runOwnedBridge(args []string)(returned error){
 executable,err:=os.Executable();if err!=nil{return err}
 input,err:=host.NewProcessBridgeInput(executable,os.Stdin);if err!=nil{return err}
 output,err:=host.NewProcessBridgeOutput(executable,os.Stdout)
 if err!=nil{return errors.Join(err,input.Cancel(),input.Wait())}
 defer host.EndBridge()
 return host.RunBridgeOwnedIO(input,output,args...)
}
