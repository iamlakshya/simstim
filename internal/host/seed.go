package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

type seedCloneArgs struct {
	BundlePath string `json:"bundle_path"`
	DestDir    string `json:"dest_dir"`
	Branch     string `json:"branch"`
	Origin     string `json:"origin,omitempty"`
	UserName   string `json:"user_name,omitempty"`
	UserEmail  string `json:"user_email,omitempty"`
}

type seedCloneResult struct {
	Head string `json:"head"`
}

// seedClone runs inside git-op (with gitSafe: hooks and fsmonitor off, version
// gate, systemd scope and timeout) to clone one uploaded laptop git bundle into
// DestDir, restore its origin URL, and configure user.name / user.email
// (spec 17.4).
func seedClone(a *seedCloneArgs) (*seedCloneResult, error) {
	if a == nil || a.BundlePath == "" || a.DestDir == "" || a.Branch == "" {
		return nil, UserError("bad_request", "seed.clone needs bundle_path, dest_dir and branch", "Update the client.")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, UserError("no_git", "git is not installed on this host", "Install git on the host.")
	}
	if err := gitVersionGate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(a.DestDir), 0o700); err != nil {
		return nil, fmt.Errorf("create seed parent: %w", err)
	}
	if _, err := git(filepath.Dir(a.DestDir), "clone", "--branch="+a.Branch, "--", a.BundlePath, a.DestDir); err != nil {
		return nil, asUserError(err, "seed_failed", "Check that the local repository has commits on its current branch.")
	}
	if a.Origin != "" {
		if _, err := git(a.DestDir, "remote", "set-url", "--", "origin", a.Origin); err != nil {
			return nil, asUserError(err, "seed_failed", "Check the origin remote URL of the local repository.")
		}
	} else {
		_, _ = git(a.DestDir, "remote", "remove", "origin")
	}
	if a.UserName != "" {
		if _, err := git(a.DestDir, "config", "--", "user.name", a.UserName); err != nil {
			return nil, asUserError(err, "seed_failed", "Check git user.name on your laptop.")
		}
	}
	if a.UserEmail != "" {
		if _, err := git(a.DestDir, "config", "--", "user.email", a.UserEmail); err != nil {
			return nil, asUserError(err, "seed_failed", "Check git user.email on your laptop.")
		}
	}
	head, err := git(a.DestDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, asUserError(err, "seed_failed", "Check the seeded repository.")
	}
	return &seedCloneResult{Head: strings.TrimSpace(head)}, nil
}

// RunStageSFTP serves SFTP over in and out rooted at the session's private
// staging folder so the client can upload copy-in files and seed bundles over
// P1.6's pkg/sftp path (spec 17.3, 17.4).
func RunStageSFTP(id string, in io.Reader, out io.Writer) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if !validID(id) {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if _, err := LoadManifest(id); err != nil {
		return UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	stageDir := paths.StageDir(id)
	if err := paths.EnsurePrivateDir(stageDir); err != nil {
		return fmt.Errorf("create staging folder: %w", err)
	}
	srv, err := sftp.NewServer(stdioRWC{Reader: in, Writer: out}, sftp.WithServerWorkingDirectory(stageDir))
	if err != nil {
		return fmt.Errorf("start stage SFTP server: %w", err)
	}
	defer srv.Close()
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("serve stage SFTP: %w", err)
	}
	return nil
}

// startDeferredSession launches a deferred session after its manifest and
// staged inputs are applied; tests may override it to inspect the final manifest.
var startDeferredSession = startSession

// StartDeferred applies staged seed bundles and copy-in files for a session
// created with DeferStart=true, cleans up the staging folder, and launches the
// session's holder or supervisor (spec 17.3, 17.4).
func StartDeferred(a api.SessionStartArgs) (*SessionInfo, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if !validID(a.ID) {
		return nil, UserError("not_found", "no session "+a.ID, "List sessions to find its ID.")
	}
	unlock, err := lockSession(a.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	m, err := LoadManifest(a.ID)
	if err != nil {
		return nil, UserError("not_found", "no session "+a.ID, "List sessions to find its ID.")
	}
	if m.SandboxOf != "" {
		return nil, UserError("bad_request",
			"session "+m.Name+" runs inside another session's sandbox and cannot use deferred start",
			"Start sessions in an existing sandbox with new --in without --copy or --seed.")
	}
	if alive(a.ID) {
		return nil, UserError("agent_running", "session "+m.Name+" is already running", "Attach to the running session.")
	}
	secretNames, err := checkSecrets(a.Secrets)
	if err != nil {
		return nil, err
	}
	if isSandboxedWall(m) {
		agent, known := agents.Find(m.Agent.Kind)
		if len(secretNames) > 0 {
			boundSecrets, exposedSecrets, err := resolveProcessSecrets(agent, known, secretNames, a.SecretBinds, a.ExposedSecrets)
			if err != nil {
				return nil, err
			}
			m.Wall.BoundSecrets = boundSecrets
			m.Wall.ExposedSecrets = exposedSecrets
		} else {
			m.Wall.BoundSecrets = nil
			m.Wall.ExposedSecrets = nil
		}
		m.Wall.SubscriptionAuth = detectSubscriptionAuth(agent, known, m.Wall.BoundSecrets, m.Wall.ExposedSecrets)
	}

	stageDir := paths.StageDir(a.ID)
	defer os.RemoveAll(stageDir)

	if len(a.Seeds) > 0 {
		if err := applyStagedSeeds(m, stageDir, a.Seeds); err != nil {
			return nil, err
		}
	}
	if len(a.CopyFiles) > 0 {
		if err := applyStagedCopies(m, stageDir, a.CopyFiles); err != nil {
			return nil, err
		}
	}
	m.Secrets = secretNames
	m.CopiesPending = false
	m.Status = Status{State: StateStarting, Since: time.Now().UTC()}
	return startDeferredSession(m, a.Secrets)
}

func applyStagedSeeds(m *Manifest, stageDir string, seeds []api.SeedRepoSpec) error {
	workBase := paths.WorkDir(m.ID)
	if err := paths.EnsurePrivateDir(workBase); err != nil {
		return err
	}
	stageRoot, err := os.OpenRoot(stageDir)
	if err != nil {
		return UserError("seed_failed", "staged seed bundles are missing", "Run the command again.")
	}
	defer stageRoot.Close()

	seenNames := map[string]bool{}
	var records []api.SeedRepoRecord
	for _, s := range seeds {
		if err := validateRepoBaseName(s.Name); err != nil {
			return err
		}
		if seenNames[s.Name] {
			return UserError("seed_conflict", fmt.Sprintf("two seeded repositories share the name %q", s.Name),
				"Seed repositories with distinct folder names.")
		}
		seenNames[s.Name] = true
		rel, err := cleanStageRel(s.BundleRel)
		if err != nil {
			return err
		}
		st, err := stageRoot.Lstat(rel)
		if err != nil || !st.Mode().IsRegular() {
			return UserError("seed_failed", fmt.Sprintf("staged bundle %s is missing", s.Name), "Run the command again.")
		}
		bundlePath := filepath.Join(stageDir, filepath.FromSlash(rel))
		destDir := filepath.Join(workBase, s.Name)
		ans, err := gitOp(gitOpSeedClone, gitOpRequest{
			Seed: &seedCloneArgs{
				BundlePath: bundlePath,
				DestDir:    destDir,
				Branch:     s.Branch,
				Origin:     s.Origin,
				UserName:   s.UserName,
				UserEmail:  s.UserEmail,
			},
		})
		if err != nil {
			return err
		}
		head := ""
		if ans.Seed != nil {
			head = ans.Seed.Head
		}
		records = append(records, api.SeedRepoRecord{
			Name:     s.Name,
			Dir:      destDir,
			Branch:   s.Branch,
			SeedHead: head,
		})
	}
	m.SeedRepos = records
	attachSeedWorkspaces(m, workBase, records)
	recordSeedHeads(m, records)
	return nil
}

func recordSeedHeads(m *Manifest, records []api.SeedRepoRecord) {
	for _, r := range records {
		if r.SeedHead == "" {
			continue
		}
		if m.SeedHeads == nil {
			m.SeedHeads = map[string]string{}
		}
		m.SeedHeads[hostToGuestPath(m, r.Dir)] = r.SeedHead
	}
}

func hostToGuestPath(m *Manifest, hostDir string) string {
	if m == nil || m.Wall == nil {
		return hostDir
	}
	if m.Wall.WorkspaceHost == hostDir && m.Wall.WorkspaceGuest != "" {
		return m.Wall.WorkspaceGuest
	}
	for _, wm := range m.Wall.ExtraRW {
		if wm.Source == hostDir && wm.Dest != "" {
			return wm.Dest
		}
	}
	for _, wm := range m.Wall.ExtraRO {
		if wm.Source == hostDir && wm.Dest != "" {
			return wm.Dest
		}
	}
	return hostDir
}

// ResolvePullGuestRepo resolves the guest workspace path and trusted seed head
// OID for walled bundle pulls, honouring --repo on multi-repo sessions
// (isolation spec 6.10, 17.4).
func ResolvePullGuestRepo(m *Manifest, wantRepo string) (guestDir, seedHead string, err error) {
	hostDir, err := resolvePullRepoDir(m, wantRepo)
	if err != nil {
		return "", "", err
	}
	guestDir = hostToGuestPath(m, hostDir)
	for _, r := range m.SeedRepos {
		if r.Dir == hostDir && r.SeedHead != "" {
			return guestDir, r.SeedHead, nil
		}
	}
	if m != nil && len(m.SeedHeads) > 0 {
		if sha := m.SeedHeads[guestDir]; sha != "" {
			return guestDir, sha, nil
		}
		if sha := m.SeedHeads[m.Cwd]; sha != "" {
			return guestDir, sha, nil
		}
	}
	return guestDir, "", nil
}

// attachSeedWorkspaces updates the session's workspace (and sandboxed-wall
// bind mounts) after cloning one or more laptop seed repositories (spec 17.4).
func attachSeedWorkspaces(m *Manifest, workBase string, records []api.SeedRepoRecord) {
	if len(records) == 0 {
		return
	}
	if isSandboxedWall(m) {
		if m.Wall.WorkspaceHost == workBase {
			m.Wall.WorkspaceHost = records[0].Dir
			m.Wall.WorkspaceGuest = process.WorkspaceGuestPath(records[0].Dir)
			m.Cwd = m.Wall.WorkspaceGuest
			for _, r := range records[1:] {
				addProcessCopyMount(m, r.Dir, process.WorkspaceGuestPath(r.Dir))
			}
			return
		}
		for _, r := range records {
			dest := process.WorkspaceGuestPath(r.Dir)
			if dest == m.Wall.WorkspaceGuest {
				dest = "/work/seed-" + r.Name
			}
			addProcessCopyMount(m, r.Dir, dest)
		}
		return
	}
	if m.Cwd == workBase && len(records) == 1 {
		m.Cwd = records[0].Dir
	}
}

func cleanStageRel(rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, '\\') || strings.ContainsRune(rel, 0) {
		return "", UserError("cp_path", "invalid staging path", "Update the client.")
	}
	c := path.Clean(rel)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", UserError("cp_path", "invalid staging path", "Update the client.")
	}
	return c, nil
}

func validateRepoBaseName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`+"\x00") || strings.ContainsFunc(name, unicode.IsControl) {
		return UserError("bad_request", fmt.Sprintf("invalid repository name %q", api.CleanText(name, 64)),
			"Seed repositories with plain folder names.")
	}
	return nil
}

// resolvePullRepoDir selects the repository directory on the host for
// RunPullBundle, honouring --repo when a session holds multiple repositories
// (spec 17.4).
func resolvePullRepoDir(m *Manifest, wantRepo string) (string, error) {
	hostCwd := m.HostDir()
	if len(m.SeedRepos) == 0 {
		if wantRepo != "" && filepath.Base(hostCwd) != wantRepo {
			return "", UserError("repo_not_found", fmt.Sprintf("session %s has no repository named %q", m.Name, api.CleanText(wantRepo, 64)),
				"Run pull without --repo, or check the session's folder.")
		}
		return hostCwd, nil
	}
	byName := make(map[string]string, len(m.SeedRepos)+1)
	var names []string
	for _, r := range m.SeedRepos {
		if r.Name != "" && r.Dir != "" {
			byName[r.Name] = r.Dir
			names = append(names, r.Name)
		}
	}
	// If the session also started in a separate host --dir that is a git repo,
	// include it alongside the seeded repositories (spec 17.4).
	if hostCwd != "" && hostCwd != paths.WorkDir(m.ID) {
		already := false
		for _, r := range m.SeedRepos {
			if r.Dir == hostCwd {
				already = true
				break
			}
		}
		if !already {
			if _, err := os.Stat(filepath.Join(hostCwd, ".git")); err == nil {
				base := filepath.Base(hostCwd)
				if _, exists := byName[base]; !exists {
					byName[base] = hostCwd
					names = append(names, base)
				}
			}
		}
	}
	sort.Strings(names)
	if wantRepo != "" {
		if dir, ok := byName[wantRepo]; ok {
			return dir, nil
		}
		return "", UserError("repo_not_found", fmt.Sprintf("session %s has no repository named %q", m.Name, api.CleanText(wantRepo, 64)),
			"Pass --repo with one of: "+strings.Join(names, ", ")+".")
	}
	if len(names) == 1 {
		return byName[names[0]], nil
	}
	return "", UserError("repo_ambiguous", fmt.Sprintf("session %s has %d repositories: %s", m.Name, len(names), strings.Join(names, ", ")),
		"Pass --repo with one of: "+strings.Join(names, ", ")+".")
}
