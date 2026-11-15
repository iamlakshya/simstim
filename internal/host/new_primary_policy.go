package host

import (
 "github.com/iamlakshya/simstim/internal/api"
 "github.com/iamlakshya/simstim/internal/egress"
)

// prepareLaptopPrimaryPolicy consumes only the checked declaration inside the
// original sealed input summary. It never refreshes a policy from host Git.
func prepareLaptopPrimaryPolicy(a NewArgs)(NewArgs,error){
 primary:=a.Inputs.Repositories[0]
 file:=api.CloneRepositoryPolicy(primary.Policy)
 if file==nil {
  if a.GrantHash!="" || len(a.GrantAnswers)!=0 {return a,UserError("bad_request","the laptop primary has no declared policy file for these answers","Prepare its exact selected commit and policy again.")};return a,nil
 }
 if !api.ValidGitOID(primary.Commit) || !api.ValidPolicyHash(file.Hash) || a.GrantHash!=file.Hash {return a,UserError("repo_policy","the laptop primary policy does not match its selected declaration","Review the policy from the exact selected laptop commit again.")}
 layer,_,err:=egress.PrepareRepositoryLayer(file,egress.RepositoryBroader(a.Layers),a.GrantHash,a.GrantAnswers);if err!=nil{return a,err}
 a.Layers=append(api.CloneLayers(a.Layers),layer)
 if _,_,err:=egress.MergeLayers(a.Layers);err!=nil{return a,err};return a,nil
}
