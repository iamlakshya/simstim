package cli

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Live diagnostics use only the accepted saved connection, with no install or
// wake. Failure leaves health unavailable, never a claim that the host is OK.
func readHostsHealthV1(ctx context.Context, hosts []client.Host) map[string]api.HostFacts {
	result := make(map[string]api.HostFacts)
	var mu sync.Mutex
	var group sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, original := range hosts {
		group.Add(1)
		go func(h client.Host) {
			defer group.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			c, err := client.Dial(client.WithoutInstall(bounded), h, false)
			if err != nil {
				return
			}
			defer c.Close()
			facts, err := c.HostFacts(bounded)
			if err != nil {
				return
			}
			mu.Lock()
			result[h.Name] = facts
			mu.Unlock()
		}(original)
	}
	group.Wait()
	return result
}

func hostHealthTextV1(facts api.HostFacts, available bool) string {
	if !available {
		return "health unavailable"
	}
	if facts.DrainKnown && facts.Drain {
		return "draining"
	}
	var rows []string
	for _, item := range facts.Health {
		if item.Severity == "warn" || item.Severity == "blocks_new" {
			rows = append(rows, client.CleanDetail(item.ID))
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, ", ")
}

func sessionRuntimeMarksV1(session client.Session) string {
	health := client.RuntimeHealth(session)
	var marks []string
	if health.Outdated {
		marks = append(marks, "outdated")
	}
	if health.Exposed {
		marks = append(marks, "exposed")
	}
	if len(marks) == 0 {
		return ""
	}
	return " [" + strings.Join(marks, ", ") + "]"
}
