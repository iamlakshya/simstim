package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/client"
)

// prepareSetPairs computes the saved change and the values shown before it.
func prepareSetPairs(ctx context.Context, h client.Host, pairs []pair) (client.Host, []string, bool, error) {
	if err := ctx.Err(); err != nil {
		return client.Host{}, nil, false, err
	}
	before := make([]string, len(pairs))
	changed := h
	if h.Machine != nil {
		m := *h.Machine
		changed.Machine = &m
	}
	saves := false
	for i, p := range pairs {
		if p.s.current != nil {
			var err error
			before[i], err = p.s.current(ctx, h)
			if err != nil {
				return client.Host{}, nil, false, err
			}
		} else {
			before[i] = p.s.now(h)
		}
		if p.s.save != nil {
			p.s.save(&changed, p.value)
			saves = true
		}
	}
	return changed, before, saves, nil
}

// recordSavedPairs reports settings only after hosts.toml has been saved.
func recordSavedPairs(pr *prompter, w io.Writer, h client.Host, pairs []pair, before []string) []string {
	var took []string
	for i, p := range pairs {
		if p.s.save == nil {
			continue
		}
		took = append(took, p.text+" in hosts.toml")
		pr.changes().Add("saved %s in hosts.toml for %s", p.text, h.Name)
		fmt.Fprintf(w, "%s: %s %s → %s.\n", h.Name, p.s.key, before[i], p.value)
	}
	return took
}
