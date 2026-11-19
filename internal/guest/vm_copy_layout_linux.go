//go:build linux

package guest

import (
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// copyTemporaryMountV1 is reached only beside actual native mount creation.
// A root-only backing parent owns both the hidden stage and the visible child;
// captured original handles do not mix a guest bind mount with another mount.
func(v *vmFiles) copyTemporaryMountV1(root *os.Root,target,name string)error{
	if v.copyNativeOwner==nil{return v.mount("tmpfs",filepath.Join(v.root,target),"tmpfs",unix.MS_NOSUID|unix.MS_NODEV,"mode=1777")}
	private:=filepath.Join("run",brand.Name,".copy-"+name)
	if err:=v.ownedDirectory(root,private,0700,0,0);err!=nil{return err}
	if err:=v.mount("tmpfs",filepath.Join(v.root,private),"tmpfs",unix.MS_NOSUID|unix.MS_NODEV,"mode=0700");err!=nil{return err}
	backing,err:=root.OpenRoot(private)
	if err!=nil{return err}
	defer backing.Close()
	if err:=v.ownedDirectory(backing,"files",01777,0,0);err!=nil{return err}
	stages,err:=v.prepareCopyStagesV1(backing)
	if err!=nil{return err}
	if err:=v.captureCopyMountV1("/"+target,backing,"files",stages);err!=nil{return err}
	return v.bind(filepath.Join(v.root,private,"files"),filepath.Join(v.root,target),false,true)
}

// copyFreshHomeV1 creates the actual private fresh-home filesystem. Its
// parent remains root-owned and unwriteable to UID1000; only /home/agent is
// writable. The same original tmpfs hosts its private staging directory.
func(v *vmFiles) copyFreshHomeV1(root *os.Root)error{
	if v.copyNativeOwner==nil{
		if err:=vmDirectory(root,"home/agent",0700);err!=nil{return err}
		return v.mount("tmpfs",filepath.Join(v.root,"home/agent"),"tmpfs",unix.MS_NOSUID|unix.MS_NODEV,"mode=0700,uid=1000,gid=1000")
	}
	if err:=v.mount("tmpfs",filepath.Join(v.root,"home"),"tmpfs",unix.MS_NOSUID|unix.MS_NODEV,"mode=0755");err!=nil{return err}
	home,err:=root.OpenRoot("home")
	if err!=nil{return err}
	defer home.Close()
	if err:=v.ownedDirectory(home,"agent",0700,AgentUID,AgentGID);err!=nil{return err}
	stages,err:=v.prepareCopyStagesV1(home)
	if err!=nil{return err}
	return v.captureCopyMountV1("/home/agent",home,"agent",stages)
}
