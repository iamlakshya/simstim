package host

import "errors"

// resumePreparationChargeV1 is DATA/native accounting only. Unlike ordinary
// Starting, a held preparation has NO time expiry; slow copy/key fetch and an
// uncertain dispatch keep the complete charge until actual native settlement.
// A live primary and its preparation reserve the same quantum once.
func resumePreparationChargeV1(m *Manifest, boot string, primary, aux memoryUnitFactsV1) (int64, bool, error) {
	if m == nil {
		return 0, false, resumePreparationDataErrorV1()
	}
	value := m.ResumePreparation
	if value == nil {
		return 0, false, nil
	}
	if err := validateResumePreparationV1Data(value); err != nil {
		return 0, false, err
	}
	if m.SandboxOf != "" || !isWalledMemoryV1(m) || m.Auxiliary != nil || aux.exists {
		return 0, false, resumePreparationDataErrorV1()
	}
	if value.Boot != boot {
		if primary.exists {
			return 0, false, resumePreparationDataErrorV1()
		}
		return 0, false, nil
	}
	declared, err := declaredUnitMemoryV1(m, primary)
	if err != nil || declared != value.MemoryMax {
		return 0, false, errors.Join(err, resumePreparationDataErrorV1())
	}
	return value.MemoryMax, true, nil
}
