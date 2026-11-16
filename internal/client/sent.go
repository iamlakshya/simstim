package client

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SentHeader names the host identity whose grant history is being shown.
// An old helper's missing ID is shown as unknown, never as an empty slice.
func SentHeader(h Host, tier Tier, hostID string) string {
	id := "unknown"
	if ValidHostID(hostID) {
		id = hostID[:4] + "…" + hostID[len(hostID)-4:]
	}
	t := tier.String()
	if t == "" {
		t = "unknown"
	}
	return fmt.Sprintf("Sent to %s (%s, host ID %s)", CleanLine(h.Name), t, id)
}

type sentTotal struct {
	count uint64
	last  time.Time
}

// SentDetails shows recorded grant kinds, including never for those with no
// active record. Removed records and other host identities are excluded.
// It uses names and counts only, with no secret or credential values.
func SentDetails(h Host, hostID string, records []GrantRecord) []Detail {
	kinds := []OutboundKind{OutboundSecret, OutboundSSHAgent, OutboundSSHKey, OutboundRepo, OutboundPaste, OutboundPush, OutboundPublicPush, OutboundRootRun}
	labels := []string{"secret", "ssh agent", "ssh keys", "repos", "pastes", "push grants", "public push grants", "root steps"}
	totals := map[OutboundKind]map[string]sentTotal{}
	for _, r := range records {
		if r.HostName != h.Name || r.RemovedAt != nil || r.Count <= 0 || hostID != "" && r.HostID != hostID {
			continue
		}
		if totals[r.Kind] == nil {
			totals[r.Kind] = map[string]sentTotal{}
		}
		v := totals[r.Kind][r.Name]
		v.count += uint64(r.Count)
		if r.LastAt.After(v.last) {
			v.last = r.LastAt
		}
		totals[r.Kind][r.Name] = v
	}
	rows := make([]Detail, len(kinds))
	for i, kind := range kinds {
		rows[i] = Detail{Label: labels[i], Value: "never"}
		byName := totals[kind]
		if len(byName) == 0 {
			continue
		}
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := byName[name]
			if v.count == 0 {
				continue
			}
			switch kind {
			case OutboundPaste:
				rows[i].Value = fmt.Sprintf("%d %s", v.count, sentPlural(v.count, "image", "images"))
			case OutboundSSHAgent:
				rows[i].Value = sentTimes(v)
			case OutboundRepo, OutboundPush, OutboundPublicPush:
				rows[i].Items = append(rows[i].Items, CleanDetail(name))
			default:
				rows[i].Items = append(rows[i].Items, CleanDetail(name)+"  "+sentTimes(v))
			}
		}
	}
	return rows
}

func sentTimes(v sentTotal) string {
	text := fmt.Sprintf("sent %d %s", v.count, sentPlural(v.count, "time", "times"))
	if !v.last.IsZero() {
		text += ", last " + v.last.Local().Format("2 Jan 15:04")
	}
	return text
}

func sentPlural(n uint64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// RotateReminder names the credentials to replace after forgetting h.
// It sorts and cleans names and says nothing when there are none.
func RotateReminder(h Host, names []string) string {
	if len(names) == 0 {
		return ""
	}
	shown := make([]string, len(names))
	for i, name := range names {
		shown[i] = CleanDetail(name)
	}
	sort.Strings(shown)
	return fmt.Sprintf("Rotate these secrets, which %s got: %s.", CleanLine(h.Name), strings.Join(shown, ", "))
}
