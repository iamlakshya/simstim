package jail

import (
	"os"
	"testing"
)

func TestFileRulesV1ClosedSlotRoleRightsAndModeGrammar(t *testing.T) {
	valid := FileRuleV1{FD: 3, Role: FileDataV1, Mode: 0o444, Access: Read}
	for name, change := range map[string]func(*FileRuleV1){
		"stdin": func(r *FileRuleV1) { r.FD = 0 }, "stderr": func(r *FileRuleV1) { r.FD = 2 }, "stray native fd": func(r *FileRuleV1) { r.FD = 80 },
		"unknown role": func(r *FileRuleV1) { r.Role = "unknown" }, "zero rights": func(r *FileRuleV1) { r.Access = 0 }, "device right": func(r *FileRuleV1) { r.Access |= Ioctl }, "socket right": func(r *FileRuleV1) { r.Access |= Connect },
		"special mode": func(r *FileRuleV1) { r.Mode |= os.ModeSetuid }, "data exec": func(r *FileRuleV1) { r.Access |= Exec }, "executable data mode": func(r *FileRuleV1) { r.Mode = 0o755 }, "readonly write": func(r *FileRuleV1) { r.Access = Write },
	} {
		t.Run(name, func(t *testing.T) {
			rule := valid
			change(&rule)
			if _, err := checkFileRulesV1([]FileRuleV1{rule}, 1); err == nil {
				t.Fatal("unsafe file rule accepted")
			}
		})
	}
	if _, err := checkFileRulesV1([]FileRuleV1{valid, valid}, 1); err == nil {
		t.Fatal("duplicate slot accepted")
	}
	for _, rule := range []FileRuleV1{valid, {FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read | Write}, {FD: 3, Role: FileProgramV1, Mode: 0o111, Access: Read | Exec}, {FD: 3, Role: FileProgramV1, Mode: 0o555, Access: Exec}} {
		if _, err := checkFileRulesV1([]FileRuleV1{rule}, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, rule := range []FileRuleV1{{FD: 3, Role: FileProgramV1, Mode: 0o755, Access: Read}, {FD: 3, Role: FileProgramV1, Mode: 0o755, Access: Write | Exec}, {FD: 3, Role: FileProgramV1, Mode: 0o777, Access: Exec}} {
		if _, err := checkFileRulesV1([]FileRuleV1{rule}, 1); err == nil {
			t.Fatal("unsafe executable grant accepted")
		}
	}
}
