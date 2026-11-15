package api

import "reflect"

// CloneRepositoryPolicy owns all selected file-policy data through the ONE
// policy clone kernel. Nil remains an absent file, never an empty allowance.
func CloneRepositoryPolicy(value *RepositoryPolicy)*RepositoryPolicy {
 if value==nil{return nil};return cloneLayerData(reflect.ValueOf(value)).Interface().(*RepositoryPolicy)
}
// Clone owns this same declared repository, including nested policy values.
func (r InputRepository) Clone()InputRepository {r.Policy=CloneRepositoryPolicy(r.Policy);return r}
// Equal compares exact declared source facts, never a host source identity.
func (r InputRepository) Equal(other InputRepository)bool{return reflect.DeepEqual(r,other)}
// CloneInputRepositories preserves order and owns every mutable policy record.
func CloneInputRepositories(values []InputRepository)[]InputRepository {
 if values==nil{return nil};out:=make([]InputRepository,len(values));for i:=range values{out[i]=values[i].Clone()};return out
}
// Clone owns the authenticated summary; the opaque digest remains unchanged.
func (s InputSummary) Clone()InputSummary{s.Repositories=CloneInputRepositories(s.Repositories);return s}
