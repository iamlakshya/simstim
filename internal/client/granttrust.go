package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/nettarget"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Proposal choices never stand in for H5's live tier/exposure consent. Records
// retain every file hash, and streaming keeps total history out of actor memory.
type grantTrustEntry struct {
	Host    string          `json:"host"`
	Repo    string          `json:"repo"`
	Hash    string          `json:"hash"`
	Answers map[string]bool `json:"answers"`
}

var grantTrustProcess = make(chan struct{}, 1)

func grantTrustPath() string { return filepath.Join(StateDir(), "grant-trust.json") }
func lockGrantTrustProcess(ctx context.Context) (func(), error) {
	select {
	case grantTrustProcess <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-grantTrustProcess
			return nil, err
		}
		return func() { <-grantTrustProcess }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// LoadGrantAnswers reads one exact version under the existing bounded locks.
// A changed hash or a missing file is not an answer. No directory is created.
func LoadGrantAnswers(ctx context.Context, host, repo, hash string) (map[string]bool, bool, error) {
	if !ValidHostName(host) || repo == "" || !api.ValidPolicyHash(hash) {
		return nil, false, badGrantTrust()
	}
	unlockProcess, err := lockGrantTrustProcess(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlockProcess()
	if _, err := os.Stat(grantTrustPath()); os.IsNotExist(err) {
		return nil, false, nil
	}
	unlock, err := lockPathContext(ctx, grantTrustPath()+".lock", 2*time.Second, true)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	var answer map[string]bool
	found := false
	err = walkGrantTrust(ctx, func(entry grantTrustEntry, _ json.RawMessage) error {
		if entry.Host == host && entry.Repo == repo && entry.Hash == hash {
			answer = maps.Clone(entry.Answers)
			found = true
		}
		return nil
	})
	return answer, found, err
}

// RememberGrantAnswers atomically keeps an explicit hash-bound answer. The
// previous file stays unchanged on cancellation or any unsupported metadata.
func RememberGrantAnswers(ctx context.Context, host string, question api.GrantQuestion, answers map[string]bool) error {
	if !ValidHostName(host) || !validGrantQuestion(question) {
		return badGrantTrust()
	}
	entry := grantTrustEntry{Host: host, Repo: question.Repo, Hash: question.Hash, Answers: map[string]bool{}}
	for _, proposal := range question.Proposals {
		answer, found := answers[proposal.ID]
		if !found {
			return badGrantTrust()
		}
		entry.Answers[proposal.ID] = answer
	}
	unlockProcess, err := lockGrantTrustProcess(ctx)
	if err != nil {
		return err
	}
	defer unlockProcess()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return fmt.Errorf("make repository answer folder: %w", err)
	}
	unlock, err := lockPathContext(ctx, grantTrustPath()+".lock", 2*time.Second, true)
	if err != nil {
		return err
	}
	defer unlock()
	return writeGrantTrust(ctx, entry)
}

func walkGrantTrust(ctx context.Context, visit func(grantTrustEntry, json.RawMessage) error) error {
	file, err := openConfig(grantTrustPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return badGrantTrust()
	}
	decoder := json.NewDecoder(file)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return badGrantTrust()
	}
	seen := map[string]bool{}
	version := 0
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err != nil {
			return badGrantTrust()
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return badGrantTrust()
		}
		seen[name] = true
		switch name {
		case "v":
			if decoder.Decode(&version) != nil || version != 1 {
				return badGrantTrust()
			}
		case "records":
			token, err := decoder.Token()
			if err != nil || token != json.Delim('[') {
				return badGrantTrust()
			}
			for decoder.More() {
				if err := ctx.Err(); err != nil {
					return err
				}
				var raw json.RawMessage
				if decoder.Decode(&raw) != nil || len(raw)+1 > proto.MaxFrame {
					return badGrantTrust()
				}
				entry, err := decodeGrantTrustEntry(raw)
				if err != nil {
					return err
				}
				if err := visit(entry, raw); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return badGrantTrust()
			}
		default:
			return badGrantTrust()
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || version != 1 || !seen["records"] {
		return badGrantTrust()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return badGrantTrust()
	}
	return nil
}

func decodeGrantTrustEntry(raw json.RawMessage) (grantTrustEntry, error) {
	var entry grantTrustEntry
	fields, err := api.JSONObjectFields(raw, "host", "repo", "hash", "answers")
	if err != nil || len(fields) != 4 || json.Unmarshal(raw, &entry) != nil || !ValidHostName(entry.Host) || entry.Repo == "" || !api.ValidPolicyHash(entry.Hash) {
		return entry, badGrantTrust()
	}
	answers, err := api.JSONObjectFields(fields["answers"])
	if err != nil {
		return entry, badGrantTrust()
	}
	entry.Answers = map[string]bool{}
	for id, value := range answers {
		var answer bool
		if id == "" || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &answer) != nil {
			return entry, badGrantTrust()
		}
		entry.Answers[id] = answer
	}
	return entry, nil
}

func writeGrantTrust(ctx context.Context, entry grantTrustEntry) error {
	file, err := os.CreateTemp(StateDir(), ".grant-trust-*")
	if err != nil {
		return fmt.Errorf("create repository answers: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := io.WriteString(file, `{"v":1,"records":[`); err != nil {
		return err
	}
	comma := ""
	err = walkGrantTrust(ctx, func(old grantTrustEntry, raw json.RawMessage) error {
		if old.Host == entry.Host && old.Repo == entry.Repo && old.Hash == entry.Hash {
			return nil
		}
		if _, err := io.WriteString(file, comma); err != nil {
			return err
		}
		if _, err := file.Write(raw); err != nil {
			return err
		}
		comma = ","
		return nil
	})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(entry)
	if err != nil || len(raw)+1 > proto.MaxFrame {
		return badGrantTrust()
	}
	if _, err := io.WriteString(file, comma); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if _, err := io.WriteString(file, `]}`); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(name, grantTrustPath()); err != nil {
		return fmt.Errorf("save repository answers: %w", err)
	}
	return nil
}

func badGrantTrust() error {
	return &UserError{Msg: "repository grant answers are unreadable or incompatible", Hint: "Review " + brand.Name + "'s grant-trust.json before starting again. No proposal was approved.", Code: "grant_trust", Kind: KindRefused}
}

// AnswerRepositoryGrants discards cached values and binds only the displayed
// proposal choices. H5 fetches/authorises values again on the fresh attempt.
func AnswerRepositoryGrants(a NewArgs, q api.GrantQuestion, answers map[string]bool) NewArgs {
	a.GrantHash = q.Hash
	a.GrantAnswers = maps.Clone(answers)
	a.Secrets = nil
	a.RepositoryPolicy = nil
	return a
}

func validGrantQuestion(q api.GrantQuestion) bool {
	if q.Repo == "" || !api.ValidPolicyHash(q.Hash) || !api.ValidLayerRef(api.LayerRef{Kind: api.LayerRepo, Name: q.Repo, Source: api.SourceRepositoryFile}) {
		return false
	}
	seen := map[string]bool{}
	for _, rule := range q.Proposals {
		if !rule.Allow || rule.Door != api.DoorNet || rule.Layer != api.LayerRepo || rule.Name != q.Repo || rule.Source != api.SourceRepositoryFile || rule.Once || !rule.Until.IsZero() || rule.Mode != "" || seen[rule.ID] {
			return false
		}
		target, err := nettarget.Parse(rule.Target)
		if err != nil {
			return false
		}
		methods, err := api.CanonicalHTTPMethods(rule.Methods)
		if err != nil {
			return false
		}
		canonical := rule
		canonical.Target, canonical.Methods = target.String(), methods
		if api.RuleID(canonical) != rule.ID {
			return false
		}
		seen[rule.ID] = true
	}
	return true
}
