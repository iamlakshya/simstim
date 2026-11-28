package host

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// ApplyCopyIn places staged copy-in files into an existing session (used on
// attach after a boot-time restore when CopiesPending is true, spec 17.3).
func ApplyCopyIn(a api.CopyApplyArgs) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if !validID(a.ID) {
		return UserError("not_found", "no session "+a.ID, "List sessions to find its ID.")
	}
	unlock, err := lockSession(a.ID)
	if err != nil {
		return err
	}
	defer unlock()

	m, err := LoadManifest(a.ID)
	if err != nil {
		return UserError("not_found", "no session "+a.ID, "List sessions to find its ID.")
	}
	stageDir := paths.StageDir(a.ID)
	defer os.RemoveAll(stageDir)

	if len(a.CopyFiles) > 0 {
		if err := applyStagedCopies(m, stageDir, a.CopyFiles); err != nil {
			return err
		}
		m.CopiesPending = false
	} else if len(m.CopyIn) == 0 {
		m.CopiesPending = false
	}
	return m.Save()
}

func isSandboxedWall(m *Manifest) bool {
	return m != nil && m.Wall != nil && (m.Wall.Driver == "process" || m.Wall.Driver == "gvisor")
}

func appendUnique(list []string, v string) []string {
	if v == "" || v == "." || slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func applyStagedCopies(m *Manifest, stageDir string, files []api.CopyInFile) error {
	stageRoot, err := os.OpenRoot(stageDir)
	if err != nil {
		return UserError("upload", "staged copy-in files are missing", "Run the command again.")
	}
	defer stageRoot.Close()

	for _, f := range files {
		rel, err := cleanStageRel(f.StageRel)
		if err != nil {
			return err
		}
		dst, err := resolveCopyTarget(m, f.GuestPath)
		if err != nil {
			return err
		}
		if err := placeFromStage(stageRoot, rel, dst, f.GuestPath, isWalled(m), m.ID); err != nil {
			return err
		}
		if isSandboxedWall(m) {
			addProcessCopyMount(m, dst, path.Clean(f.GuestPath))
		}
	}
	return nil
}

func addProcessCopyMount(m *Manifest, hostDst, guestPath string) {
	if m.Wall.WorkspaceGuest != "" {
		if _, inside := trimGuestPrefix(guestPath, m.Wall.WorkspaceGuest); inside {
			return
		}
	}
	for _, em := range m.Wall.ExtraRW {
		if em.Dest == guestPath {
			return
		}
	}
	m.Wall.ExtraRW = append(m.Wall.ExtraRW, WallMount{Source: hostDst, Dest: guestPath})
}

func placeFromStage(stageRoot *os.Root, stageRel, dst, guestPath string, walled bool, sessionID string) error {
	st, err := stageRoot.Lstat(stageRel)
	if err != nil {
		return fmt.Errorf("stat staged %s: %w", stageRel, err)
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return UserError("cp_path", "refused symbolic link in staged files", "Copy regular files and folders only.")
	}
	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return UserError("cp_path", "refused non-regular staged file", "Copy regular files and folders only.")
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("create target folder for %s: %w", guestPath, err)
		}
		dstRoot, err := os.OpenRoot(filepath.Dir(dst))
		if err != nil {
			return fmt.Errorf("open target folder for %s: %w", guestPath, err)
		}
		defer dstRoot.Close()
		if err := copyFileBetweenRoots(stageRoot, stageRel, dstRoot, filepath.Base(dst), st.Mode().Perm()); err != nil {
			return err
		}
		if walled {
			appendCopyInAudit(sessionID, guestPath)
		}
		return nil
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("create target folder %s: %w", guestPath, err)
	}
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open target folder %s: %w", guestPath, err)
	}
	defer dstRoot.Close()
	return copyDirBetweenRoots(stageRoot, stageRel, dstRoot, "", guestPath, walled, sessionID)
}

func copyDirBetweenRoots(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel, guestBase string, walled bool, sessionID string) error {
	entries, err := fs.ReadDir(srcRoot.FS(), srcRel)
	if err != nil {
		return fmt.Errorf("read staged folder %s: %w", srcRel, err)
	}
	for _, e := range entries {
		childSrc := srcRel + "/" + e.Name()
		childDst := e.Name()
		if dstRel != "" {
			childDst = dstRel + "/" + e.Name()
		}
		fi, err := srcRoot.Lstat(childSrc)
		if err != nil {
			return fmt.Errorf("stat staged %s: %w", childSrc, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		if fi.IsDir() {
			if err := dstRoot.MkdirAll(childDst, 0o700); err != nil {
				return fmt.Errorf("create folder %s: %w", childDst, err)
			}
			if err := copyDirBetweenRoots(srcRoot, childSrc, dstRoot, childDst, guestBase, walled, sessionID); err != nil {
				return err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if err := copyFileBetweenRoots(srcRoot, childSrc, dstRoot, childDst, fi.Mode().Perm()); err != nil {
			return err
		}
		if walled {
			appendCopyInAudit(sessionID, path.Join(guestBase, childDst))
		}
	}
	return nil
}

func copyFileBetweenRoots(srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, perm fs.FileMode) error {
	if perm == 0 {
		perm = 0o600
	}
	in, err := srcRoot.Open(srcRel)
	if err != nil {
		return fmt.Errorf("open staged %s: %w", srcRel, err)
	}
	defer in.Close()
	if parent := path.Dir(dstRel); parent != "." {
		if err := dstRoot.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create parent %s: %w", parent, err)
		}
	}
	out, err := dstRoot.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", dstRel, err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("write %s: %w", dstRel, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", dstRel, closeErr)
	}
	return nil
}

func resolveCopyTarget(m *Manifest, guestPath string) (string, error) {
	if !strings.HasPrefix(guestPath, "/") || strings.ContainsRune(guestPath, 0) || strings.ContainsFunc(guestPath, unicode.IsControl) {
		return "", UserError("cp_path", fmt.Sprintf("invalid guest copy path %q", api.CleanText(guestPath, 120)),
			"Use an absolute path or ~/… for the guest destination.")
	}
	clean := path.Clean(guestPath)
	if clean == "/" || strings.HasPrefix(clean, "/..") {
		return "", UserError("cp_path", fmt.Sprintf("invalid guest copy path %q", api.CleanText(guestPath, 120)),
			"Use a file or folder path inside /home/agent, /work or /mnt.")
	}
	const guestHome = "/home/agent"
	if rest, ok := trimGuestPrefix(clean, guestHome); ok {
		if isWalled(m) {
			return filepath.Join(paths.SessionDir(m.ID), "home", filepath.FromSlash(rest)), nil
		}
		h, err := os.UserHomeDir()
		if err != nil || h == "" {
			return "", fmt.Errorf("resolve host home directory: %w", err)
		}
		return filepath.Join(h, filepath.FromSlash(rest)), nil
	}
	if m.Wall != nil && m.Wall.WorkspaceHost != "" && m.Wall.WorkspaceGuest != "" {
		if rest, ok := trimGuestPrefix(clean, m.Wall.WorkspaceGuest); ok {
			return filepath.Join(m.Wall.WorkspaceHost, filepath.FromSlash(rest)), nil
		}
	}
	if rest, ok := trimGuestPrefix(clean, "/work"); ok {
		base := m.Cwd
		if m.Wall != nil && m.Wall.WorkspaceHost != "" {
			base = m.Wall.WorkspaceHost
		}
		return filepath.Join(base, filepath.FromSlash(rest)), nil
	}
	if rest, ok := trimGuestPrefix(clean, "/mnt"); ok {
		return filepath.Join(paths.SessionDir(m.ID), "mnt", filepath.FromSlash(rest)), nil
	}
	if isWalled(m) {
		return filepath.Join(paths.SessionDir(m.ID), "files", filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
	}
	return filepath.FromSlash(clean), nil
}

func trimGuestPrefix(clean, prefix string) (string, bool) {
	if clean == prefix {
		return "", true
	}
	if rest, ok := strings.CutPrefix(clean, prefix+"/"); ok {
		return rest, true
	}
	return "", false
}

func appendCopyInAudit(sessionID, guestPath string) {
	f, err := os.OpenFile(paths.AuditLog(sessionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(struct {
		Time    string `json:"time"`
		Event   string `json:"event"`
		Session string `json:"session"`
		Detail  string `json:"detail"`
	}{
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Event:   "copy_in",
		Session: sessionID,
		Detail:  api.CleanText(guestPath, 256),
	})
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}
