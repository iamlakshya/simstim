package supervisor

import "github.com/iamlakshya/simstim/internal/inputtransfer"

// retainedInputLocked chooses the one existing current-run operation. It
// creates no second receipt store and refuses an ambiguous duplicate owner.
// Its caller holds s.mu and separately verifies protected summary and phase.
func (s *Supervisor) retainedInputLocked(scope inputtransfer.Scope,run string)*startupInput {
 var selected *startupInput
 for _,input:=range []*startupInput{s.startupInput,s.lateInput}{
  if input==nil || input.scope!=scope || input.runGeneration!=run {continue}
  if selected!=nil && selected!=input{return nil};selected=input
 }
 return selected
}
