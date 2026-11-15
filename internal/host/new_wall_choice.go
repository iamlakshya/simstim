package host

import "github.com/iamlakshya/simstim/internal/wall"

// validateChosenWall preserves the original client's floor intent. It grants
// no host runtime, local tier or downgrade permission of its own.
func validateChosenWall(a NewArgs)error {
 if a.Wall=="" || a.Wall=="auto" {return nil}
 level,err:=wall.ParseLevel(a.Wall);if err!=nil{return err}
 if a.Floor!="" {
  floor,err:=wall.ParseLevel(a.Floor);if err!=nil{return UserError("bad_wall","the original sandbox floor is invalid","Choose a supported wall floor in the original client.")}
  if level<floor && (!a.Downgrade || a.DowngradedFrom==""){return UserError("wall_floor","the selected wall is below the original floor without its explicit downgrade","Review and approve this named downgrade in the original client.")}
 }
 if a.DowngradedFrom!="" {
  prior,err:=wall.ParseLevel(a.DowngradedFrom)
  if err!=nil || !a.Downgrade || prior<=level{return UserError("wall_floor","the recorded sandbox downgrade is inconsistent","Resolve this exact wall choice again in the original client.")}
 }
 return nil
}
