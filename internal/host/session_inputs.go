package host

import (
	"path"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
)

// CloneInputSummary retains only an owned authenticated input declaration.
func CloneInputSummary(summary *api.InputSummary) *api.InputSummary {
	if summary == nil {
		return nil
	}
	copy := *summary
	copy.Repositories = slices.Clone(summary.Repositories)
	return &copy
}

func validateNewInputs(a NewArgs) error {
	if a.Inputs == nil {
		return nil
	}
	if a.Wall == "" || a.Wall == "none" {
		return UserError("not_walled", "session inputs need a sandbox", "Choose a walled session before copying files or repositories.")
	}
	policy, _, err := egress.MergeLayers(a.Layers)
	if err != nil {
		return err
	}
	copyMax, seedMax, err := api.InputLimits(policy.Sandbox)
	if err != nil {
		return err
	}
	return a.Inputs.Validate(copyMax, seedMax)
}

func addLaptopRepositories(m *Manifest, summary *api.InputSummary, hostPrimary bool) error {
	if summary == nil {
		return nil
	}
	copyMax, seedMax, err := SessionInputLimits(m)
	if err != nil {
		return err
	}
	if err := summary.Validate(copyMax, seedMax); err != nil {
		return err
	}
	if m.Files == nil {
		m.Files = &FilePlan{}
	}
	var repositories []Seed
	if hostPrimary {
		repositories = append(repositories, m.Files.Seed)
	}
	seen := map[string]bool{}
	for _, repository := range repositories {
		seen[repository.Repo] = true
	}
	if m.SeedHeads == nil {
		m.SeedHeads = map[string]string{}
	}
	for _, repository := range repositories {
		if !repository.Scratch && api.ValidGitOID(repository.Commit) {
			m.SeedHeads[repository.Repo] = repository.Commit
		}
	}
	for _, repository := range summary.Repositories {
		if seen[repository.Name] {
			return UserError("seed_plan", "the selected repositories have the same basename", "Choose source repositories with distinct basenames.")
		}
		seen[repository.Name] = true
		seed := Seed{Laptop: true, Repo: repository.Name, SourceBranch: repository.SourceBranch, Commit: repository.Commit, Origin: repository.Origin, BundleSize: repository.Size,
			Branch: brand.Name + "/" + m.ID + "/" + Slug(repository.Name)}
		repositories = append(repositories, seed)
		// Only the original authenticated client facts create seed heads.
		m.SeedHeads[repository.Name] = repository.Commit
	}
	if len(repositories) > 0 {
		m.Files.Repositories, m.Files.PrimaryRepo = repositories, repositories[0].Repo
		m.Files.Seed = repositories[0]
		m.Cwd = path.Join("/work", repositories[0].Repo)
	}
	m.Inputs = CloneInputSummary(summary)
	m.CopiesPending = summary.FileCount > 0
	return nil
}

// SessionInputLimits resolves only the protected canonical policy's numbers.
// It grants no local file paths or authority to refresh a client source.
func SessionInputLimits(m *Manifest) (int64, int64, error) {
	var values api.SandboxValues
	if m != nil && m.Policy != nil {
		values = m.Policy.Sandbox
	}
	return api.InputLimits(values)
}

func validateResumeInputs(m *Manifest, summary *api.InputSummary) error {
	if summary == nil {
		if m.Inputs != nil && m.Inputs.FileCount > 0 {
			return UserError("input_required", "this client-driven wake needs its declared local input plan", "Use the current client and its original approved local input plan.")
		}
		return nil
	}
	if m.Wall == nil || len(summary.Repositories) != 0 {
		return UserError("seed_plan", "waking an existing sandbox accepts only copied inputs", "Keep the protected repositories and provide the current approved copy plan.")
	}
	copyMax, seedMax, err := SessionInputLimits(m)
	if err != nil {
		return err
	}
	if err := summary.Validate(copyMax, seedMax); err != nil {
		return err
	}
	if m.Inputs != nil && summary.Nonce == m.Inputs.Nonce {
		return UserError("input_scope", "the wake reused an earlier input nonce", "Create a fresh locally approved input operation before waking the sandbox.")
	}
	return nil
}
