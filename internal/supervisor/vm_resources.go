package supervisor

import (
	"context"
	"reflect"
	"runtime"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// Run claims the actual native supervisor before accepting an original outside
// operation loan. A context or saved Manifest cannot issue runtime authority.
func Run(id string) error {
	if err:=secretmem.Protect();err!=nil{return err}
	return runSupervisor(context.Background(),id)
}

// RunWithExpectedSpec retains the caller's lifetime. ExpectedSpec is data only;
// the same actual outside startup loan remains mandatory before construction.
func RunWithExpectedSpec(owner context.Context,id string) error {
	if owner==nil{return execRefusal("the supervisor has no owner lifetime")}
	return runSupervisor(owner,id)
}

func (s *Supervisor) lifetimeContext() context.Context {
	if s.lifetime!=nil{return s.lifetime}
	return context.Background()
}

func (s *Supervisor) bootLimit() time.Duration {
	if s.m!=nil && s.m.Wall!=nil && (s.m.Wall.Driver=="chv" || s.m.Wall.Driver=="firecracker") {return microvm.BootTimeout}
	return bootTimeout
}

// protectedVMDriver selects only from the canonical decoded protected Need,
// paired with its actual outside-issued receipt. Metadata never issues a boot.
func (s *Supervisor) protectedVMDriver() (string,error) {
	if host.OriginalVMNeedFromContext(s.lifetimeContext())==nil{return "",execRefusal("the microvm has no original native operation receipt")}
	s.mu.Lock();m:=host.CloneManifest(*s.m);s.mu.Unlock()
	need,err:=vmneed.DecodeV1(m.VMNeedV1,runtime.GOARCH)
	if err!=nil{return "",err}
	if m.Wall==nil || need.ValidateWall(runtime.GOARCH,m.Wall.Level,m.Wall.Driver,m.Limits.MemoryMax)!=nil{return "",execRefusal("the protected microvm Need conflicts with its selected wall or cap")}
	spec,known:=wall.ExpectedSpec(s.lifetimeContext())
	selected,err:=host.ExpectedOriginalRuntime(s.lifetimeContext())
	if err!=nil{return "",err}
	if !known || spec.ID!=s.id || spec.SessionDir!=paths.SessionDir(s.id) || spec.VM==nil || spec.MemoryMax!=m.Limits.MemoryMax || spec.CPUs!=need.CPUs || spec.DiskMax!=need.DiskMax || !reflect.DeepEqual(selected,need.Runtime) || !reflect.DeepEqual(*spec.VM,need.BootV1) {return "",execRefusal("the selected microvm differs from its original protected boot projection")}
	if need.Driver!="chv" && need.Driver!="firecracker"{return "",execRefusal("the selected microvm driver has no complete native factory")}
	return need.Driver,nil
}

func (s *Supervisor) sandboxForRun() (sandbox,error) {
	if s.m.Wall==nil{return nil,execRefusal("the supervisor has no selected wall")}
	if s.m.Wall.Driver!="chv" && s.m.Wall.Driver!="firecracker" {
		return sandboxFor(s.m)
	}
	if _,err:=s.protectedVMDriver();err!=nil{return nil,err}
	return s.prepareVMResources()
}

func (s *Supervisor) publishVMReferences(ctx context.Context,spec wall.Spec) error {
	if err:=ctx.Err();err!=nil{return err}
	s.saveMu.Lock();defer s.saveMu.Unlock()
	var snapshot host.Manifest
	return s.writeManifestChange(&snapshot,false,nil,nil,func(m *host.Manifest)error{return host.SetVMReferences(m,spec)},func(saved *host.Manifest){
		s.m.Run.Image,s.m.Run.Label=saved.Run.Image,saved.Run.Label
		s.m.Run.Artefacts=slices.Clone(saved.Run.Artefacts)
	})
}

// save holds saveMu. Preserve the same actual factory's published retention
// references when an older ordinary snapshot reaches the strong writer later.
func (s *Supervisor) mergeVMReferences(target *host.Manifest) {
	if target.Wall==nil || target.Wall.Driver!="chv" && target.Wall.Driver!="firecracker" {return}
	s.mu.Lock();defer s.mu.Unlock()
	target.Run.Image,target.Run.Label=s.m.Run.Image,s.m.Run.Label
	target.Run.Artefacts=slices.Clone(s.m.Run.Artefacts)
}
