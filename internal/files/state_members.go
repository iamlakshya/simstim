package files

// FamilyMembers expands validated state file declarations through the SAME
// family suffix table used by stopped copy/restore/capture. A caller may use
// these owned relative names to fix metadata on its fresh, inaccessible home;
// expansion never opens a path or supplies filesystem authority.
func FamilyMembers(names []string)([]string,error){
 if err:=(StatePlan{Files:names}).Validate();err!=nil{return nil,err}
 var members []string
 seen:=make(map[string]bool)
 for _,name:=range names{for _,suffix:=range sidecars{member:=name+suffix;if !seen[member]{seen[member]=true;members=append(members,member)}}}
 return members,nil
}
