package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func validInstallVersionV1(version string) bool {
	return version != "" && version != "." && version != ".." && len(version) <= 128 && strings.Trim(version, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == ""
}
func validInstallItemV1(item api.InstallItemV1) bool {
	if item.Size <= 0 || item.Size > 512<<20 || !supply.IsHex64(item.SHA256) {
		return false
	}
	return item.Kind == supply.KindHelper && item.Name == brand.HostBinary || item.Kind == supply.KindVT && item.Name == brand.VTBinary || item.Kind == supply.KindUserns && item.Name == brand.UsernsBinary
}

func readInstallPlanV1(input io.Reader) (api.InstallPlanV1, error) {
	var plan api.InstallPlanV1
	var prefix [9]byte
	if _, err := io.ReadFull(input, prefix[:]); err != nil {
		return plan, err
	}
	length, err := strconv.ParseUint(string(prefix[:8]), 16, 32)
	if err != nil || length == 0 || length > 16<<10 || prefix[8] != '\n' || fmt.Sprintf("%08x", length) != string(prefix[:8]) {
		return plan, placementError("the installation plan header is malformed")
	}
	raw := make([]byte, int(length))
	if _, err := io.ReadFull(input, raw); err != nil {
		return plan, err
	}
	if !api.ExactJSONFields(raw, plan) || json.Unmarshal(raw, &plan) != nil || plan.Version != 1 || !ValidHostID(plan.Nonce) || !validInstallVersionV1(plan.HelperVersion) || plan.Arch != runtime.GOARCH || len(plan.Items) == 0 || len(plan.Items) > 3 {
		return plan, placementError("the installation plan has unknown roles, architecture or version")
	}
	seen := map[string]bool{}
	for _, item := range plan.Items {
		if !validInstallItemV1(item) || seen[item.Name] {
			return plan, placementError("the installation plan repeats or changes a carried file role")
		}
		seen[item.Name] = true
	}
	if !seen[brand.HostBinary] {
		return plan, placementError("the installation plan names no original helper")
	}
	if plan.Items[len(plan.Items)-1].Kind != supply.KindHelper {
		return plan, placementError("the helper must publish after its other carried files")
	}
	return plan, nil
}

// RunInstallPayload is a closed raw-input command. It never enters ordinary
// bridge dispatch, starts a session or accepts a caller-selected command/path.
// The client checked this staged helper's bytes before starting it.
func RunInstallPayload(ctx context.Context, input io.Reader, output io.Writer) (returned error) {
	defer func() {
		if returned != nil {
			var user *proto.Error
			if !errors.As(returned, &user) {
				returned = UserError("install_helper", "the native helper installation did not finish: "+returned.Error(), "Keep the host state and staging files for inspection, then retry through the original client.")
			}
		}
	}()
	if ctx == nil {
		return placementError("the closed installer has no original invocation lifetime")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	plan, err := readInstallPlanV1(input)
	if err != nil {
		return err
	}
	if err := CheckCaller(); err != nil {
		return err
	}
	stage, err := checkStagedInstallerV1(ctx, plan)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, stage.close()) }()
	owner, err := placementForInstall()
	if err != nil {
		return err
	}
	publisher, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, publisher.close()) }()
	ctx = publisher.ctx
	for _, path := range []string{paths.DataDir(), paths.ConfigDir(), paths.RuntimeDir()} {
		root, err := owner.createInstallerRootV1(ctx, path)
		if err != nil {
			return err
		}
		if err := root.Close(); err != nil {
			return err
		}
	}
	versionPath := filepath.Join(paths.BinDir(), plan.HelperVersion)
	root, err := owner.createInstallerRootV1(ctx, versionPath)
	if err != nil {
		return err
	}
	defer joinInstallCloseV1(&returned, root)
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	dirInfo, err := directory.Stat()
	_ = directory.Close()
	if err != nil {
		return err
	}
	for _, item := range plan.Items {
		if err := installCarriedFileV1(ctx, publisher, root, dirInfo, plan.HelperVersion, item, input); err != nil {
			return err
		}
	}
	// Exactly the declared bytes are consumed. A trailing input byte is not
	// another plan, additional file or shell continuation.
	var extra [1]byte
	if n, err := input.Read(extra[:]); n != 0 || err != io.EOF {
		return placementError("the installation stream has trailing or unfinished input")
	}
	if err := installCurrentLinkV1(ctx, publisher, plan.HelperVersion); err != nil {
		return err
	}
	_, err = io.WriteString(output, "__INSTALLED__\n")
	return err
}

func checkStagedInstallerV1(ctx context.Context, plan api.InstallPlanV1) (*installStageV1, error) {
	name, mapped, err := mappedExecutable()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if filepath.Dir(name) != filepath.Join(home, "."+brand.Name+"-install-"+plan.Nonce) || filepath.Base(name) != brand.HostBinary {
		return nil, placementError("the closed installer is not its original staged executable")
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(name), false)
	if err != nil {
		return nil, err
	}
	file, err := cachefs.OpenFile(root, brand.HostBinary, os.O_RDONLY, 0o755)
	if err != nil {
		root.Close()
		return nil, err
	}
	fail := func(err error) (*installStageV1, error) { file.Close(); root.Close(); return nil, err }
	info, err := file.Stat()
	if err != nil || !os.SameFile(mapped, info) {
		return fail(placementError("the staged helper differs from the running mapped executable"))
	}
	for _, item := range plan.Items {
		if item.Kind == supply.KindHelper {
			if err := supply.CheckOpenContext(ctx, supply.Item{Kind: item.Kind, Name: item.Name, Arch: plan.Arch, SHA256: item.SHA256, Size: item.Size}, file); err != nil {
				return fail(err)
			}
			dir, err := root.Open(".")
			if err != nil {
				return fail(err)
			}
			parent, err := dir.Stat()
			dir.Close()
			if err != nil {
				return fail(err)
			}
			return &installStageV1{root: root, file: file, directory: refOf(parent), executable: refOf(info), nonce: plan.Nonce}, nil
		}
	}
	return fail(placementError("the running installer has no original helper digest"))
}

func installCarriedFileV1(ctx context.Context, publisher *stateMetadataPublisher, root *os.Root, directory os.FileInfo, version string, item api.InstallItemV1, input io.Reader) error {
	nonce, err := footprintNonce()
	if err != nil {
		return err
	}
	partName := ".install-part-" + nonce
	file, err := cachefs.OpenFile(root, partName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	creator, err := cachefs.CaptureFile(root, partName, file, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if info, err := root.Lstat(partName); err == nil {
			if native, err := file.Stat(); err == nil && (native.Mode().Perm() == 0o600 || native.Mode().Perm() == 0o755) && creator.CheckCreatedFile(root, partName, file, native.Mode().Perm()) == nil && os.SameFile(info, native) {
				_ = root.Remove(partName)
			}
		}
	}()
	hash := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(file, hash), input, item.Size); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return UserError("checksum", "a carried helper file failed its original SHA-256 check", "Install this client again before retrying the helper installation.")
	}
	if err := file.Chmod(0o755); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	info, err := cachefs.ValidateFile(root, partName, file, 0o755)
	if err != nil {
		return err
	}
	return withInstallerControlV1(publisher, func(control *placementControl, guard *os.Root) error {
		ledger, err := readInstallLedgerV1(ctx, guard, control)
		if err != nil {
			return err
		}
		record, exists := ledger.Versions[version]
		if exists && !record.Directory.matches(directory) {
			return placementError("the helper version directory changed its original native identity")
		}
		if !exists {
			record = installVersionV1{Directory: refOf(directory), Files: map[string]installFileV1{}}
			_, record.Created = publisher.operation.owner.installerCreated[filepath.Join(paths.BinDir(), version)]
		}
		if current, err := root.Lstat(item.Name); err == nil {
			prior, known := record.Files[item.Name]
			if !known || prior.Item != item || !prior.Ref.matches(current) {
				return placementError("preserved an existing carried file without the same original producer receipt")
			}
			existing, err := cachefs.OpenFile(root, item.Name, os.O_RDONLY, 0o755)
			if err != nil {
				return err
			}
			check := supply.CheckOpenContext(ctx, supply.Item{Kind: item.Kind, Name: item.Name, Arch: runtime.GOARCH, SHA256: item.SHA256, Size: item.Size}, existing)
			closeErr := existing.Close()
			if check != nil || closeErr != nil {
				return errors.Join(check, closeErr)
			}
			prior.Prepared = ""
			record.Files[item.Name] = prior
			ledger.Versions[version] = record
			return writeInstallLedgerV1(ctx, guard, control, ledger)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		record.Files[item.Name] = installFileV1{Item: item, Ref: refOf(info), Prepared: partName}
		ledger.Versions[version] = record
		if err := writeInstallLedgerV1(ctx, guard, control, ledger); err != nil {
			return err
		}
		if err := cachefs.RenameNoReplace(root, partName, item.Name); err != nil {
			return err
		}
		if _, err := cachefs.ValidateFile(root, item.Name, file, 0o755); err != nil {
			return err
		}
		if err := syncCapturedDirectory(root); err != nil {
			return err
		}
		entry := record.Files[item.Name]
		entry.Prepared = ""
		record.Files[item.Name] = entry
		ledger.Versions[version] = record
		return writeInstallLedgerV1(ctx, guard, control, ledger)
	})
}
