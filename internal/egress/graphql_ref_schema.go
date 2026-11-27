package egress

import "context"

func gqlObject(value gqlValue, names ...string) (map[string]gqlValue, error) {
	if value.kind != '{' {
		return nil, errServiceShape
	}
	for key := range value.object {
		known := false
		for _, name := range names {
			known = known || key == name
		}
		if !known {
			return nil, errServiceShape
		}
	}
	return value.object, nil
}
func gqlStringField(fields map[string]gqlValue, name string, required bool) (string, error) {
	value, present := fields[name]
	if !present || value.kind == 'Z' {
		if required {
			return "", errServiceShape
		}
		return "", nil
	}
	if value.kind != 'S' {
		return "", errServiceShape
	}
	return value.text, nil
}
func gqlIDField(fields map[string]gqlValue, name string, required bool) error {
	value, present := fields[name]
	if !present || value.kind == 'Z' {
		if required {
			return errServiceShape
		}
		return nil
	}
	if value.kind != 'S' && value.kind != 'I' || value.text == "" {
		return errServiceShape
	}
	return nil
}
func gqlBoolField(fields map[string]gqlValue, name string) error {
	value, present := fields[name]
	if !present || value.kind == 'Z' {
		return nil
	}
	if value.kind != 'B' {
		return errServiceShape
	}
	return nil
}
func gqlOptionalStrings(fields map[string]gqlValue, names ...string) error {
	for _, name := range names {
		if _, err := gqlStringField(fields, name, false); err != nil {
			return err
		}
	}
	return nil
}

func gqlCommitMessage(value gqlValue) error {
	fields, err := gqlObject(value, "headline", "body")
	if err != nil {
		return err
	}
	if _, err := gqlStringField(fields, "headline", true); err != nil {
		return err
	}
	_, err = gqlStringField(fields, "body", false)
	return err
}
func gqlFileChanges(value gqlValue) error {
	if value.kind == 'Z' {
		return nil
	}
	fields, err := gqlObject(value, "additions", "deletions")
	if err != nil {
		return err
	}
	for _, name := range []string{"additions", "deletions"} {
		entries, present := fields[name]
		if !present || entries.kind == 'Z' {
			continue
		}
		list, err := gqlInputList(entries)
		if err != nil {
			return err
		}
		for _, entry := range list {
			names := []string{"path"}
			if name == "additions" {
				names = append(names, "contents")
			}
			fields, err := gqlObject(entry, names...)
			if err != nil {
				return err
			}
			for _, key := range names {
				if _, err := gqlStringField(fields, key, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func inspectGraphQLMutation(ctx context.Context, selection gqlSelection, provided map[string]gqlValue, definitions map[string]gqlVariable, prefix string) (bool, bool, error) {
	known := false
	for _, name := range []string{"createCommitOnBranch", "createRef", "updateRef", "updateRefs", "deleteRef", "mergeBranch", "mergePullRequest"} {
		known = known || selection.name == name
	}
	if !known {
		return false, false, nil
	} // This row is not a new global mutation gate.
	if len(selection.args) != 1 {
		return false, true, errServiceShape
	}
	input, present := selection.args["input"]
	if !present {
		return false, true, errServiceShape
	}
	input, err := gqlResolve(ctx, input, provided, definitions, 0)
	if err != nil {
		return false, true, err
	}
	var names []string
	switch selection.name {
	case "createCommitOnBranch":
		names = []string{"branch", "clientMutationId", "expectedHeadOid", "fileChanges", "message"}
	case "createRef":
		names = []string{"clientMutationId", "name", "oid", "repositoryId"}
	case "updateRef":
		names = []string{"clientMutationId", "force", "oid", "refId"}
	case "updateRefs":
		names = []string{"clientMutationId", "refUpdates", "repositoryId"}
	case "deleteRef":
		names = []string{"clientMutationId", "refId"}
	case "mergeBranch":
		names = []string{"authorEmail", "base", "clientMutationId", "commitMessage", "head", "repositoryId"}
	case "mergePullRequest":
		names = []string{"authorEmail", "clientMutationId", "commitBody", "commitHeadline", "expectedHeadOid", "mergeMethod", "pullRequestId"}
	}
	fields, err := gqlObject(input, names...)
	if err != nil {
		return false, true, err
	}
	if err := gqlOptionalStrings(fields, "clientMutationId"); err != nil {
		return false, true, err
	}
	switch selection.name {
	case "createCommitOnBranch":
		if _, err := gqlStringField(fields, "expectedHeadOid", true); err != nil {
			return false, true, err
		}
		if err := gqlCommitMessage(fields["message"]); err != nil {
			return false, true, err
		}
		if changes, present := fields["fileChanges"]; present {
			if err := gqlFileChanges(changes); err != nil {
				return false, true, err
			}
		}
		branch, err := gqlObject(fields["branch"], "branchName", "id", "repositoryNameWithOwner")
		if err != nil {
			return false, true, err
		}
		id, byID := branch["id"]
		byID = byID && id.kind != 'Z'
		name, err := gqlStringField(branch, "branchName", false)
		if err != nil {
			return false, true, err
		}
		repo, err := gqlStringField(branch, "repositoryNameWithOwner", false)
		if err != nil {
			return false, true, err
		}
		if byID {
			if err := gqlIDField(branch, "id", true); err != nil || name != "" || repo != "" {
				return false, true, errServiceShape
			}
			return true, true, nil
		}
		if name == "" || repo == "" {
			return false, true, errServiceShape
		}
		return !serviceOwnBranch(name, prefix), true, nil
	case "createRef":
		if err := gqlIDField(fields, "repositoryId", true); err != nil {
			return false, true, err
		}
		if _, err := gqlStringField(fields, "oid", true); err != nil {
			return false, true, err
		}
		name, err := gqlStringField(fields, "name", true)
		if err != nil {
			return false, true, err
		}
		return !serviceOwnRef(name, prefix), true, nil
	case "updateRef":
		if err := gqlIDField(fields, "refId", true); err != nil {
			return false, true, err
		}
		if _, err := gqlStringField(fields, "oid", true); err != nil {
			return false, true, err
		}
		if err := gqlBoolField(fields, "force"); err != nil {
			return false, true, err
		}
		return true, true, nil
	case "deleteRef":
		if err := gqlIDField(fields, "refId", true); err != nil {
			return false, true, err
		}
		return true, true, nil // Opaque IDs do not establish a namespace.
	case "updateRefs":
		if err := gqlIDField(fields, "repositoryId", true); err != nil {
			return false, true, err
		}
		updates, present := fields["refUpdates"]
		if !present {
			return false, true, errServiceShape
		}
		list, err := gqlInputList(updates)
		if err != nil {
			return false, true, err
		}
		needPath := false
		for _, update := range list {
			fields, err := gqlObject(update, "afterOid", "beforeOid", "force", "name")
			if err != nil {
				return false, true, err
			}
			name, err := gqlStringField(fields, "name", true)
			if err != nil {
				return false, true, err
			}
			if _, err := gqlStringField(fields, "afterOid", true); err != nil {
				return false, true, err
			}
			if _, err := gqlStringField(fields, "beforeOid", false); err != nil {
				return false, true, err
			}
			if err := gqlBoolField(fields, "force"); err != nil {
				return false, true, err
			}
			needPath = needPath || !serviceOwnRef(name, prefix)
		}
		return needPath, true, nil
	case "mergeBranch":
		if err := gqlIDField(fields, "repositoryId", true); err != nil {
			return false, true, err
		}
		base, err := gqlStringField(fields, "base", true)
		if err != nil {
			return false, true, err
		}
		if _, err := gqlStringField(fields, "head", true); err != nil {
			return false, true, err
		}
		if err := gqlOptionalStrings(fields, "authorEmail", "commitMessage"); err != nil {
			return false, true, err
		}
		return !serviceOwnBranch(base, prefix), true, nil
	case "mergePullRequest":
		if err := gqlIDField(fields, "pullRequestId", true); err != nil {
			return false, true, err
		}
		if err := gqlOptionalStrings(fields, "authorEmail", "commitBody", "commitHeadline", "expectedHeadOid"); err != nil {
			return false, true, err
		}
		if method, present := fields["mergeMethod"]; present && method.kind != 'Z' {
			if method.kind != 'E' && method.kind != 'S' || method.text != "MERGE" && method.text != "SQUASH" && method.text != "REBASE" {
				return false, true, errServiceShape
			}
		}
		return true, true, nil
	}
	return false, true, errServiceShape
}

func gqlInputList(value gqlValue) ([]gqlValue, error) {
	if value.kind == 'Z' || value.kind == 0 {
		return nil, errServiceShape
	}
	if value.kind == '[' {
		return value.list, nil
	}
	return []gqlValue{value}, nil
}
