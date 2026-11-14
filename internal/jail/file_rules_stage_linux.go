package jail

// Distinct stage names make a frozen older helper reject the request before
// execution. Its JSON decoder ignored unknown fields, so a field alone is
// insufficient for a security-sensitive protocol extension.
const (
	stageOuterFilesV1 = "outer-files-v1"
	stageInitFilesV1  = "init-files-v1"
	stageExecFilesV1  = "exec-files-v1"
)

func fileRulesStageV1(stage string, c *setup) string {
	if c.FilesV1Version != 1 {
		return stage
	}
	switch stage {
	case stageOuter:
		return stageOuterFilesV1
	case stageInit:
		return stageInitFilesV1
	case stageExec:
		return stageExecFilesV1
	default:
		return stage
	}
}

func checkFileRulesStageV1(stage string, c *setup) (string, error) {
	normal := stage
	v1 := true
	switch stage {
	case stageOuterFilesV1:
		normal = stageOuter
	case stageInitFilesV1:
		normal = stageInit
	case stageExecFilesV1:
		normal = stageExec
	default:
		v1 = false
	}
	if v1 {
		if c.FilesV1Version != 1 || len(c.FileRulesV1) == 0 {
			return "", failure("an inherited-file v1 stage has no matching versioned grants", "Use one helper version for every jail stage.")
		}
	} else if c.FilesV1Version != 0 || len(c.FileRulesV1) != 0 {
		return "", failure("a legacy jail stage cannot carry inherited-file grants", "Use one helper version with inherited-file v1 support.")
	}
	return normal, nil
}
