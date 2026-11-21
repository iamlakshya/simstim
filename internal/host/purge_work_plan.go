package host

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func purgeWorkUnknownV1(message string) error {
	return UserError("purge_work_unknown", message, "Keep the sandbox, pull its committed work and copy its remaining files before retrying purge.")
}

// The original protected file plan and recorded pull receipts choose the
// repositories and saved commits. Guest metadata cannot add an exclusion.
func protectedPurgeWorkPlanV1(snapshot *Manifest) (proto.PurgeWorkInspectArgsV1, error) {
	var args proto.PurgeWorkInspectArgsV1
	if snapshot == nil || !api.ValidSessionID(snapshot.ID) || snapshot.SandboxOf != "" || snapshot.Files == nil || len(snapshot.Files.Mounts) != 0 {
		return args, purgeWorkUnknownV1("the original sandbox has no supported protected workspace plan")
	}
	seeds := snapshot.Files.Repositories
	if len(seeds) == 0 {
		seeds = []Seed{snapshot.Files.Seed}
	} else if !reflect.DeepEqual(snapshot.Files.Seed, seeds[0]) || snapshot.Files.PrimaryRepo != seeds[0].Repo {
		return args, purgeWorkUnknownV1("the protected primary repository differs from its clone plan")
	}
	if len(seeds) > 128 {
		return args, purgeWorkUnknownV1("the protected workspace plan exceeds the inspection bound")
	}
	for _, oid := range snapshot.Pulled {
		if !api.ValidGitOID(oid) {
			return args, purgeWorkUnknownV1("a protected pull receipt is not an exact commit")
		}
	}
	args.Version, args.Primary = 1, snapshot.ID
	seen := map[string]bool{}
	for _, seed := range seeds {
		if !api.InputRepoName(seed.Repo) || seen[seed.Repo] {
			return args, purgeWorkUnknownV1("the protected workspace has an invalid or repeated repository name")
		}
		seen[seed.Repo] = true
		initial := seed.Commit
		if recorded, ok := snapshot.SeedHeads[seed.Repo]; ok {
			if recorded != initial {
				return args, purgeWorkUnknownV1("the protected seed receipt differs from its original repository plan")
			}
			initial = recorded
		}
		if initial == "" && !seed.Scratch || initial != "" && !api.ValidGitOID(initial) {
			return args, purgeWorkUnknownV1("the original repository has no valid protected seed history")
		}
		saved := slices.Clone(snapshot.Pulled)
		if initial != "" {
			saved = append(saved, initial)
		}
		slices.Sort(saved)
		saved = slices.Compact(saved)
		args.Repositories = append(args.Repositories, proto.PurgeWorkRepositoryV1{Name: seed.Repo, Scratch: seed.Scratch, Saved: saved})
	}
	for name, oid := range snapshot.SeedHeads {
		if !seen[name] || !api.ValidGitOID(oid) {
			return args, purgeWorkUnknownV1("the protected seed inventory contains an unknown repository or commit")
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil || len(encoded) > 48<<10 {
		return args, purgeWorkUnknownV1("the protected workspace history exceeds the inspection request bound")
	}
	return args, nil
}

func decodePurgeWorkResultV1(raw json.RawMessage, expected proto.PurgeWorkInspectArgsV1) (bool, error) {
	var result proto.PurgeWorkInspectResultV1
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 || result.Primary != expected.Primary || len(result.Repositories) != len(expected.Repositories) {
		return false, purgeWorkUnknownV1("the fixed guest inspection did not return the complete original workspace set")
	}
	fields, err := api.JSONObjectFields(raw, "v", "primary", "outside_repositories", "repositories")
	if err != nil || len(fields) != 4 || !purgeWorkBooleanV1(fields["outside_repositories"]) {
		return false, purgeWorkUnknownV1("the fixed guest inspection omitted an explicit workspace observation")
	}
	var repositories []json.RawMessage
	if json.Unmarshal(fields["repositories"], &repositories) != nil || len(repositories) != len(expected.Repositories) {
		return false, purgeWorkUnknownV1("the fixed guest inspection omitted its original repository observations")
	}
	unsaved := result.OutsideRepositories
	for index, repository := range result.Repositories {
		fields, err := api.JSONObjectFields(repositories[index], "name", "dirty", "unpulled")
		if err != nil || len(fields) != 3 || !purgeWorkBooleanV1(fields["dirty"]) || bytes.Equal(bytes.TrimSpace(fields["unpulled"]), []byte("null")) {
			return false, purgeWorkUnknownV1("the fixed guest inspection omitted an explicit repository observation")
		}
		if repository.Name != expected.Repositories[index].Name {
			return false, purgeWorkUnknownV1("the fixed guest inspection changed a protected repository identity")
		}
		unsaved = unsaved || repository.Dirty || repository.Unpulled != 0
	}
	return unsaved, nil
}

func purgeWorkBooleanV1(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	return bytes.Equal(value, []byte("true")) || bytes.Equal(value, []byte("false"))
}
