package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/onboard"
)

// The guided setup's prompts read through answer: one line at a time,
// straight from the terminal, with no reader left behind, since an
// attached session needs every byte of stdin. Ctrl+C ends the context, and
// a read waiting for a line returns at once, even one that cannot be
// cancelled.

// readLine reads one line from in, without its spaces. A cancel, a signal
// or the end of ctx, returns context.Canceled, also when it came with the
// line. A closed input with nothing on the line is an error.
func readLine(ctx context.Context, in io.Reader) (string, error) {
	line, err := answer(ctx, in)
	if err != nil {
		return "", context.Canceled
	}
	if line == "" {
		return "", fmt.Errorf("read the answer: %w", io.EOF)
	}
	return strings.TrimSpace(line), nil
}

// ask prints a question and reads the answer.
func (g *guide) ask(q string) (string, error) {
	fmt.Fprint(g.out, q)
	return readLine(g.ctx, g.in)
}

// yesNo asks a yes-or-no question. An empty answer is def.
func (g *guide) yesNo(q string, def bool) (bool, error) {
	opts := " [y/N] "
	if def {
		opts = " [Y/n] "
	}
	a, err := g.ask(q + opts)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(a) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

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
