//go:build linux || darwin

package main

import "testing"

func TestBridgeIOClosedProductionRoles(t *testing.T){
 if commands["bridge"]==nil{t.Fatal("missing actual bridge entrypoint")}
 for _,name:=range []string{"bridge-input","bridge-output"}{
  run:=commands[name];if run==nil{t.Fatal("missing private bridge resource entrypoint",name)}
  if err:=run([]string{"unexpected"});err==nil{t.Fatal("private IO role accepted arguments",name)}
 }
}
