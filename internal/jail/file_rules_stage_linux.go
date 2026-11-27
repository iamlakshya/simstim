package jail

import (
	"errors"
	"strconv"
)

// Distinct stage names make a frozen older helper reject the request before
// execution. Its JSON decoder ignored unknown fields, so a field alone is
// insufficient for a security-sensitive protocol extension.
const (
	stageOuterFilesV1           = "outer-files-v1"
	stageInitFilesV1            = "init-files-v1"
	stageExecFilesV1            = "exec-files-v1"
	stageOuterBindsV1           = "outer-binds-v1"
	stageInitBindsV1            = "init-binds-v1"
	stageExecBindsV1            = "exec-binds-v1"
	stageInitGvisorV1           = "init-gvisor-v1"
	stageExecGvisorV1           = "exec-gvisor-v1"
	stageInitGvisorControllerV1 = "init-gvisor-controller-v1"
	stageExecGvisorControllerV1 = "exec-gvisor-controller-v1"
)

func fileRulesStageV1(stage string, c *setup) string {
	selected := baseFileRulesStageV1(stage, c)
	if c.InitLeaseV1Version == 1 && stage == stageInit {
		selected = stageSlotInitLeaseV1
	}
	return publicationFileRulesStageV1(vmmFileRulesStageV1(selected, c), c)
}

func baseFileRulesStageV1(stage string, c *setup) string {
	if c.GvisorV1Version == 1 {
		if c.ControllerPublicV1 != nil {
			switch stage {
			case stageInit:
				return stageInitGvisorControllerV1
			case stageExec:
				return stageExecGvisorControllerV1
			}
		}
		switch stage {
		case stageInit:
			return stageInitGvisorV1
		case stageExec:
			return stageExecGvisorV1
		}
	}
	if c.BindsV1Version == 1 {
		switch stage {
		case stageOuter:
			return stageOuterBindsV1
		case stageInit:
			return stageInitBindsV1
		case stageExec:
			return stageExecBindsV1
		}
	}
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
	stage, modeErr := checkVMMFileRulesStageV1(stage, c)
	if modeErr != nil {
		return "", modeErr
	}
	stage, publicationErr := checkPublicationFileRulesStageV1(stage, c)
	if publicationErr != nil {
		return "", publicationErr
	}
	stage, leaseErr := checkSlotInitLeaseStageV1(stage, c)
	if leaseErr != nil {
		return "", leaseErr
	}
	normal := stage
	v1 := true
	bindsV1 := false
	gvisorV1 := false
	controllerV1 := false
	switch stage {
	case stageInitGvisorControllerV1:
		normal, bindsV1, gvisorV1, controllerV1 = stageInit, true, true, true
	case stageExecGvisorControllerV1:
		normal, bindsV1, gvisorV1, controllerV1 = stageExec, true, true, true
	case stageInitGvisorV1:
		normal, bindsV1, gvisorV1 = stageInit, true, true
	case stageExecGvisorV1:
		normal, bindsV1, gvisorV1 = stageExec, true, true
	case stageOuterBindsV1:
		normal, bindsV1 = stageOuter, true
	case stageInitBindsV1:
		normal, bindsV1 = stageInit, true
	case stageExecBindsV1:
		normal, bindsV1 = stageExec, true
	case stageOuterFilesV1:
		normal = stageOuter
	case stageInitFilesV1:
		normal = stageInit
	case stageExecFilesV1:
		normal = stageExec
	default:
		v1 = false
	}
	hasSupplied := false
	for _, bound := range c.Binds {
		if bound.AnonymousV1 && !bound.SuppliedV1 {
			return "", failure("an anonymous bind has no supplied native source", "Use one current helper for the complete jail lifecycle.")
		}
		hasSupplied = hasSupplied || bound.SuppliedV1
	}
	if controllerV1 {
		if c.ControllerPublicV1 == nil {
			return "", gvisorHandoffFailureV2("the closed controller stage lost its public state")
		}
		if c.VMMExecV1 != nil || c.PublicationLeaseV1 != nil || c.InitLeaseV1Version != 0 {
			return "", gvisorHandoffFailureV2("the closed controller cannot mix another private execution purpose")
		}
		code, extent, err := mappedControllerExtentV1(*c.ControllerPublicV1)
		if err != nil || c.Files != extent-3 || c.ExecFD != code+1 || len(c.Argv) != 2 || c.Argv[0] != "/proc/self/fd/"+strconv.Itoa(code+1) || c.Argv[1] != "native-controller-setup-v1" {
			return "", errors.Join(err, gvisorHandoffFailureV2("the fixed controller stage changed its exact public executable or vector"))
		}
	} else if c.ControllerPublicV1 != nil {
		return "", gvisorHandoffFailureV2("a legacy or generic stage cannot adopt the fixed controller public state")
	}
	if gvisorV1 && !controllerV1 {
		return "", gvisorHandoffFailureV2("the legacy generic gvisor executable purpose is retired")
	}
	if gvisorV1 {
		if c.GvisorV1Version != 1 || !c.Userns || c.Sub != nil || c.Net != NetNone || c.Seccomp != profileGvisorV1 || len(c.Binds) == 0 {
			return "", failure("the gvisor stage lost its closed namespace purpose", "Use the original host gvisor factory and one helper version.")
		}
		for _, bound := range c.Binds {
			if !bound.SuppliedV1 {
				return "", failure("the gvisor stage cannot reopen raw paths as mount authority", "Keep the original captured source vector.")
			}
		}
	} else if c.GvisorV1Version != 0 || c.Seccomp == profileGvisorV1 {
		return "", failure("a generic jail stage cannot adopt the gvisor bootstrap purpose", "Use one current helper for the complete original boot.")
	}
	if bindsV1 {
		if c.BindsV1Version != 1 || !hasSupplied {
			return "", failure("a supplied-bind stage has no matching versioned source capabilities", "Use the same current helper for all jail stages.")
		}
		if c.FilesV1Version == 1 && len(c.FileRulesV1) > 0 || c.FilesV1Version == 0 && len(c.FileRulesV1) == 0 {
			return normal, nil
		}
		return "", failure("a supplied-bind stage has inconsistent inherited-file grants", "Keep the original versioned jail setup unchanged.")
	}
	if c.BindsV1Version != 0 || hasSupplied {
		return "", failure("a legacy jail stage cannot consume supplied source capabilities", "Use a helper with supplied-bind v1 support.")
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
