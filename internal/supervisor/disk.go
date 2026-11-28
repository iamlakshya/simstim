package supervisor

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

// defaultDiskFreePercent is the minimum free filesystem percentage required
// under the session folder before the supervisor stops the sandbox (spec 17.1,
// question 101).
const defaultDiskFreePercent = 5

// checkDisk enforces the soft disk_max cap and the 5% host free-space floor
// on walled sessions (spec 17.1). Called periodically from tickLoop.
func (s *Supervisor) checkDisk(now time.Time) {
	interval := s.opt.DiskCheckInterval
	if interval <= 0 {
		interval = time.Minute
	}
	if !s.lastDiskCheck.IsZero() && now.Sub(s.lastDiskCheck) < interval {
		return
	}
	s.lastDiskCheck = now

	s.mu.Lock()
	if s.ended || s.stopping {
		s.mu.Unlock()
		return
	}
	diskMax := s.m.Limits.DiskMax
	s.mu.Unlock()

	sessDir := paths.SessionDir(s.id)
	if diskMax > 0 {
		used := dirSize(sessDir)
		if used > diskMax {
			detail := fmt.Sprintf("session used %d bytes, above disk_max %d bytes", used, diskMax)
			s.triggerDiskFull(detail)
			return
		}
	}

	minPct := s.opt.DiskFreePercent
	if minPct <= 0 {
		minPct = defaultDiskFreePercent
	}
	diskFn := s.opt.DiskUsageFunc
	if diskFn == nil {
		diskFn = host.DiskUsage
	}
	if avail, total, ok := diskFn(sessDir); ok && total > 0 && avail*100 < total*int64(minPct) {
		detail := fmt.Sprintf("host filesystem has %d%% free space (below %d%%)", avail*100/total, minPct)
		s.triggerDiskFull(detail)
	}
}

func (s *Supervisor) triggerDiskFull(detail string) {
	s.log.Warn("stopping sandbox: disk_full", "detail", detail)
	s.audit.Record(AuditEntry{Event: "disk_full", Session: s.id, Detail: detail})
	s.mu.Lock()
	s.alertEvent(alerts.DiskFull)
	s.mu.Unlock()
	s.requestStop("disk_full: "+detail, true)
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort walk skips entries that vanish while measuring
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
			total += fi.Size()
		}
		return nil
	})
	return total
}
