package host

import "time"

func sessionCommand(m *Manifest) (string, string, error) {
	if len(m.AutomaticRestore) > 0 {
		if m.Wall == nil {
			return "", "", automaticRestoreError()
		}
		return "supervise-restore-v1", "30", nil
	}
	if m.Wall != nil {
		return "supervise", "30", nil
	}
	return "holder", "15", nil
}

func waitAutomaticController(id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		m, err := LoadManifest(id)
		if err != nil {
			return err
		}
		if len(m.AutomaticRestore) == 0 && m.Life != nil && m.Life.Phase == LifeRunning && holderPendingOrAlive(id, m) {
			return nil
		}
		if m.Life != nil && m.Life.End != nil && m.Life.End.Kind == EndBoot {
			return automaticRestoreError()
		}
		if !time.Now().Before(deadline) {
			return automaticRestoreError()
		}
		time.Sleep(20 * time.Millisecond)
	}
}
