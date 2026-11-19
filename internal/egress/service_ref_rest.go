package egress

import (
	"context"
	"errors"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

var errServiceShape = errors.New("unsupported service request shape")

// This is the existing ONE JSON projection over the already owned bounded
// request body. Finite reviewed field names reject duplicates, case aliases
// and unknown input shapes without copying unselected file content.
func serviceBodyFields(ctx context.Context, body []byte, names []string) (*api.JSONProjection, map[string]api.JSONSpan, error) {
	p, err := api.NewJSONBytesProjection(ctx, body)
	if err != nil {
		return nil, nil, err
	}
	root, err := p.ObjectRoot()
	if err != nil {
		return nil, nil, err
	}
	fields := make(map[string]api.JSONSpan, len(names))
	err = p.Object(root, names, func(field api.JSONField) error {
		if field.Known < 0 {
			return errServiceShape
		}
		fields[names[field.Known]] = field.Value
		return nil
	})
	return p, fields, err
}

func serviceString(p *api.JSONProjection, fields map[string]api.JSONSpan, name string, required, retain bool) (string, error) {
	span, present := fields[name]
	if !present {
		if required {
			return "", errServiceShape
		}
		return "", nil
	}
	var text strings.Builder
	err := p.WalkString(span, func(r rune) error {
		if retain {
			text.WriteRune(r)
		}
		return nil
	})
	return text.String(), err
}

func serviceBool(p *api.JSONProjection, fields map[string]api.JSONSpan, name string) error {
	span, present := fields[name]
	if !present {
		return nil
	}
	value, err := p.Literal(span, "true")
	if err != nil || value {
		return err
	}
	value, err = p.Literal(span, "false")
	if err != nil {
		return err
	}
	if !value {
		return errServiceShape
	}
	return nil
}

func serviceGitIdentity(p *api.JSONProjection, span api.JSONSpan, required bool) error {
	names := []string{"name", "email", "date"}
	fields := make(map[string]api.JSONSpan, len(names))
	if err := p.Object(span, names, func(field api.JSONField) error {
		if field.Known < 0 {
			return errServiceShape
		}
		fields[names[field.Known]] = field.Value
		return nil
	}); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := serviceString(p, fields, name, required && name != "date", false); err != nil {
			return err
		}
	}
	return nil
}

func serviceOwnBranch(branch, prefix string) bool {
	return sessionGitRef([]byte("refs/heads/"+branch), "refs/heads/"+prefix)
}

func serviceOwnRef(ref, prefix string) bool { return sessionGitRef([]byte(ref), "refs/heads/"+prefix) }

// Ref namespace is a service barrier with the existing explicit endpoint
// grant exception. It supplies no repo/public/credential or network authority.
// A shape error never becomes permission even with such a grant.
func inspectRESTRefWrite(ctx context.Context, request Request, kind string, body []byte, prefix string) (bool, error) {
	if request.query != "" || request.forceQuery {
		return false, errServiceShape
	}
	var names []string
	switch kind {
	case "ref_contents":
		names = []string{"message", "content", "sha", "branch", "committer", "author"}
	case "ref_create":
		names = []string{"ref", "sha"}
	case "ref_update":
		names = []string{"sha", "force"}
	case "ref_delete":
		if len(body) != 0 {
			return false, errServiceShape
		}
		ref, ok := serviceURLRef(request.Path)
		if !ok {
			return false, errServiceShape
		}
		return !serviceOwnRef("refs/"+ref, prefix), nil
	case "ref_merge":
		names = []string{"base", "head", "commit_message"}
	case "ref_pull_merge":
		names = []string{"commit_title", "commit_message", "sha", "merge_method"}
	case "ref_pull_merge_async":
		names = []string{"commit_title", "commit_message", "sha", "merge_method", "merge_action", "bypass_rules"}
	case "ref_upstream":
		names = []string{"branch"}
	case "ref_rename":
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(request.Path, "/"), "/"), "/")
		if len(parts) < 6 || !strings.EqualFold(parts[3], "branches") || !strings.EqualFold(parts[len(parts)-1], "rename") {
			return false, errServiceShape
		}
		names = []string{"new_name"}
	default:
		return false, errServiceShape
	}
	p, fields, err := serviceBodyFields(ctx, body, names)
	if err != nil {
		return false, err
	}
	switch kind {
	case "ref_contents":
		if request.Method != "PUT" && request.Method != "DELETE" {
			return false, errServiceShape
		}
		if _, err := serviceString(p, fields, "message", true, false); err != nil {
			return false, err
		}
		if _, err := serviceString(p, fields, "sha", request.Method == "DELETE", false); err != nil {
			return false, err
		}
		if _, err := serviceString(p, fields, "content", request.Method == "PUT", false); err != nil {
			return false, err
		}
		if request.Method == "DELETE" {
			if _, present := fields["content"]; present {
				return false, errServiceShape
			}
		}
		for _, name := range []string{"committer", "author"} {
			if span, present := fields[name]; present {
				if err := serviceGitIdentity(p, span, request.Method == "PUT"); err != nil {
					return false, err
				}
			}
		}
		branch, err := serviceString(p, fields, "branch", false, true)
		if err != nil {
			return false, err
		}
		return !serviceOwnBranch(branch, prefix), nil
	case "ref_create":
		if request.Method != "POST" {
			return false, errServiceShape
		}
		if _, err := serviceString(p, fields, "sha", true, false); err != nil {
			return false, err
		}
		ref, err := serviceString(p, fields, "ref", true, true)
		if err != nil {
			return false, err
		}
		return !serviceOwnRef(ref, prefix), nil
	case "ref_update":
		if request.Method != "PATCH" {
			return false, errServiceShape
		}
		if _, err := serviceString(p, fields, "sha", true, false); err != nil {
			return false, err
		}
		if err := serviceBool(p, fields, "force"); err != nil {
			return false, err
		}
		ref, ok := serviceURLRef(request.Path)
		if !ok {
			return false, errServiceShape
		}
		return !serviceOwnRef("refs/"+ref, prefix), nil
	case "ref_merge":
		for _, name := range names {
			if _, err := serviceString(p, fields, name, name != "commit_message", false); err != nil {
				return false, err
			}
		}
	case "ref_pull_merge", "ref_pull_merge_async":
		for _, name := range []string{"commit_title", "commit_message", "sha"} {
			if _, err := serviceString(p, fields, name, false, false); err != nil {
				return false, err
			}
		}
		method, err := serviceString(p, fields, "merge_method", false, true)
		if err != nil {
			return false, err
		}
		if _, present := fields["merge_method"]; present && method != "merge" && method != "squash" && method != "rebase" {
			return false, errServiceShape
		}
		if kind == "ref_pull_merge_async" {
			action, err := serviceString(p, fields, "merge_action", false, true)
			if err != nil {
				return false, err
			}
			if _, present := fields["merge_action"]; present && action != "default" && action != "direct_merge" && action != "merge_queue" {
				return false, errServiceShape
			}
			if err := serviceBool(p, fields, "bypass_rules"); err != nil {
				return false, err
			}
		}
	case "ref_upstream":
		if _, err := serviceString(p, fields, "branch", true, false); err != nil {
			return false, err
		}
	case "ref_rename":
		if _, err := serviceString(p, fields, "new_name", true, false); err != nil {
			return false, err
		}
	}
	// These reviewed merge/rename endpoints are explicitly listed protected
	// operations, rather than a guessed scope from an opaque pull request ID.
	return true, nil
}

func serviceURLRef(path string) (string, bool) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/"), "/")
	if len(parts) < 6 || !strings.EqualFold(parts[0], "repos") || !strings.EqualFold(parts[3], "git") || !strings.EqualFold(parts[4], "refs") {
		return "", false
	}
	ref := strings.Join(parts[5:], "/")
	return ref, ref != ""
}
