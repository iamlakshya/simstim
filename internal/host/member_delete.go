package host

import (
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// DeleteNamedGroup preserves both the confirmed name and the complete child
// membership. It uses the same operation lock and deletion implementation.
func DeleteNamedGroup(id, name string, members []string) (DeleteResult, error) {
	var result DeleteResult
	if err := CheckCaller(); err != nil {
		return result, err
	}
	if !validID(id) {
		return result, memberError("no valid session was named")
	}
	err := deleteSessionDeclared(id, &name, &members, &result)
	return result, err
}
func checkDeleteMembers(m *Manifest, expected *[]string) ([]*Manifest, error) {
	if m == nil || m.Wall == nil || m.SandboxOf != "" {
		if expected != nil && len(*expected) > 0 {
			return nil, memberError("only a primary has sandbox children")
		}
		return nil, nil
	}
	children, err := SandboxMembers(m.ID)
	if err != nil {
		return nil, err
	}
	if expected == nil {
		if len(children) > 0 {
			return nil, UserError("upgrade_required", "the primary has other sandbox sessions", "Use a current client to list and confirm every session before deleting this sandbox.")
		}
		return children, nil
	}
	ids := make([]string, 0, len(children))
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	slices.Sort(ids)
	want := slices.Clone(*expected)
	slices.Sort(want)
	for i, id := range want {
		if !api.ValidSessionID(id) || i > 0 && want[i-1] == id {
			return nil, memberError("the confirmed sandbox member list is invalid")
		}
	}
	if !slices.Equal(ids, want) {
		return nil, UserError("sandbox_changed", "the sandbox's sessions changed after the list was shown", "List them again and confirm the new group before deleting.")
	}
	return children, nil
}
func stopDeletedMember(m *Manifest) error {
	if !socketPresent(m.ID) {
		return nil
	}
	_, err := HolderRequestWithFeatures(m.ID, "member.rm", nil, killMemberWait, api.FeatureSpawn)
	return err
}

const killMemberWait = 8 * time.Second

func removeSavedChildren(primary string, children []*Manifest) error {
	for _, child := range children {
		var ignored DeleteResult
		if err := deleteChildUnderPrimary(primary, child.ID, nil, &ignored); err != nil {
			return err
		}
	}
	return nil
}
