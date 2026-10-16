package client

import (
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
)

// The host can answer a new session with a question, not a session: a
// UserError with the question's code, and the question as its data. The
// client asks the user, and starts again with the answer in NewArgs.

// FolderQuestion asks whether a new session's missing folder may be made.
type FolderQuestion = api.FolderQuestion

// FolderQuestionOf returns the question in an error from a new session whose
// folder does not exist on the host.
func FolderQuestionOf(err error) (FolderQuestion, bool) {
	return questionOf(err, api.CodeNoFolder, func(q FolderQuestion) bool { return q.Folder != "" })
}

// SetupQuestionOf returns the question in an error from a new session whose
// repository has a setup script the user has not answered for.
func SetupQuestionOf(err error) (SetupQuestion, bool) {
	return questionOf(err, api.CodeSetupTrust, func(q SetupQuestion) bool { return q.Repo != "" })
}

// questionOf returns the question in err when err has the question's code,
// and its data is a whole question of type Q.
func questionOf[Q any](err error, code string, whole func(Q) bool) (Q, bool) {
	var ue *UserError
	var q Q
	if !errors.As(err, &ue) || ue.Code != code || json.Unmarshal(ue.Data, &q) != nil || !whole(q) {
		return q, false
	}
	return q, true
}
