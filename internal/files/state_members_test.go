package files

import (
 "reflect"
 "testing"
)

func TestFamilyMembersUsesCompleteCanonicalFamilyAndOwnsNames(t *testing.T){
 names:=[]string{"state/database"}
 members,err:=FamilyMembers(names);if err!=nil{t.Fatal(err)}
 expected:=[]string{"state/database","state/database-wal","state/database-shm","state/database-journal"}
 if !reflect.DeepEqual(members,expected){t.Fatal("family differs from stopped snapshot",members)}
 names[0]="changed"
 if !reflect.DeepEqual(members,expected){t.Fatal("caller changed expanded metadata plan")}
}

func TestFamilyMembersRejectsUnsafeStateBeforeReturningPaths(t *testing.T){
 for _,names:=range [][]string{{"../escape"},{"/absolute"},{"a/../../escape"}}{
  if members,err:=FamilyMembers(names);err==nil || members!=nil{t.Fatal("unsafe state expanded",names,members)}
 }
}
