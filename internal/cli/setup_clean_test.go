package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestSetupQuestionsCleanEveryHostField(t *testing.T) {
	bad := "\x1bc\x1b]52;c;YQ\x07\u202e"
	for _, folder := range []bool{false, true} {
		for _, input := range []bool{false, true} {
			var out bytes.Buffer
			p := testPrompts("n\n", &out)
			p.on = input
			var err error
			if folder {
				_, err = askMakeFolder(p, "HOST\nHLINE"+bad, client.FolderQuestion{Folder: "FOLDER\rFLINE" + bad})
			} else {
				_, err = askSetup(p, "HOST\nHLINE"+bad, client.SetupQuestion{Repo: "REPO\rRLINE" + bad, Script: "SCRIPT\nSLINE" + bad}, client.SetupNotTrusted)
			}
			var ue *client.UserError
			text := out.String()
			if errors.As(err, &ue) {
				text += ue.Msg
			}
			for _, unsafe := range []string{"\x1b", "\x07", "\r", "\u202e", "\nHLINE", "\nSLINE"} {
				if strings.Contains(text, unsafe) {
					t.Errorf("folder=%v input=%v retained %q in prompt/message", folder, input, unsafe)
				}
			}
		}
	}
}
