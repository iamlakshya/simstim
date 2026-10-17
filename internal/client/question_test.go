package client

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestQuestionOf(t *testing.T) {
	setup, _ := json.Marshal(api.SetupQuestion{Repo: "/r", Script: "/r/s"})
	folder, _ := json.Marshal(api.FolderQuestion{Folder: "/home/me/new"})
	q, ok := SetupQuestionOf(&UserError{Msg: "m", Code: api.CodeSetupTrust, Data: setup})
	if !ok || q.Repo != "/r" || q.Script != "/r/s" {
		t.Fatalf("setup question %+v %v", q, ok)
	}
	f, ok := FolderQuestionOf(&UserError{Msg: "m", Code: api.CodeNoFolder, Data: folder})
	if !ok || f.Folder != "/home/me/new" {
		t.Fatalf("folder question %+v %v", f, ok)
	}
	for _, err := range []error{nil, errors.New("x"), &UserError{Msg: "m", Code: "other"},
		&UserError{Code: api.CodeSetupTrust}, &UserError{Code: api.CodeNoFolder},
		&UserError{Code: api.CodeSetupTrust, Data: folder}, &UserError{Code: api.CodeNoFolder, Data: setup}} {
		if _, ok := SetupQuestionOf(err); ok {
			t.Errorf("%#v is not a setup question", err)
		}
		if _, ok := FolderQuestionOf(err); ok {
			t.Errorf("%#v is not a folder question", err)
		}
	}
}
