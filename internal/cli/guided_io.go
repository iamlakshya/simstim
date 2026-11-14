package cli

import (
	"fmt"
	"strconv"

	"github.com/iamlakshya/simstim/internal/onboard"
)

// The guided setup's questions are one row of the prompt table (qSetup).
// They go through the prompts of prompt.go. The setup asks them only when
// input is on, and when input is off it fails at its start unless its
// flags answer every question (check).

// prompter asks the setup's questions.
func (g *guide) prompter() *prompter {
	return &prompter{ctx: g.ctx, in: g.in, out: g.out, on: true}
}

// ask prints a question and reads the answer.
func (g *guide) ask(q string) (string, error) { return g.prompter().ask(q) }

// yesNo asks a yes-or-no question. An empty answer is def.
func (g *guide) yesNo(q string, def bool) (bool, error) { return g.prompter().yesNo(q, def) }

// pick asks for a number from 1 to n, again until it gets one. With text,
// any other answer that is not empty comes back as it is.
func (g *guide) pick(q string, n int, text bool) (int, string, error) {
	for {
		a, err := g.ask(q)
		if err != nil {
			return 0, "", err
		}
		if i, err := strconv.Atoi(a); err == nil && i >= 1 && i <= n {
			return i, "", nil
		}
		if text && a != "" {
			return 0, a, nil
		}
		fmt.Fprintf(g.out, "Type a number from 1 to %d.\n", n)
	}
}

// heading starts a numbered part of the setup.
func (g *guide) heading(title string) {
	g.part++
	fmt.Fprintf(g.out, "\n%d. %s\n\n", g.part, title)
}

// steps prints things to do, with each command indented under its words.
func (g *guide) steps(ss []onboard.Step) {
	for _, s := range ss {
		if s.Say != "" {
			fmt.Fprintf(g.out, "  %s\n", s.Say)
		}
		if s.Run != "" {
			fmt.Fprintf(g.out, "      %s\n", s.Run)
		}
	}
}
