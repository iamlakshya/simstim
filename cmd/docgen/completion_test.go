package main

import (
	"strings"
	"testing"
)

func TestCompletionExamplesUseTheirShell(t *testing.T) {
	r := root()
	for _, tc := range []struct{ shell, language string }{{"powershell", "powershell"}, {"bash", "sh"}, {"zsh", "sh"}, {"fish", "sh"}} {
		cmd, _, err := r.Find([]string{"completion", tc.shell})
		if err != nil {
			t.Fatal(err)
		}
		page := commandPage(cmd)
		if !strings.Contains(page, "## Examples\n\n```"+tc.language+"\n") {
			t.Errorf("%s example has the wrong language:\n%s", tc.shell, page)
		}
	}
}
