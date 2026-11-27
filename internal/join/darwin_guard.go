//go:build darwin

package join

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/join/code"
)

// macGuardPolicy is root-owned original join policy data. No API accepts a
// value of this type as a startup source. RunDarwinVMGuard opens its own fixed
// root path and checks native uid/account/directory and immutable bytes.
type macGuardPolicy struct {
	Version     int          `json:"v"`
	Account     string       `json:"account"`
	Owner       string       `json:"owner"`
	UID         int          `json:"uid"`
	GID         int          `json:"gid"`
	GUID        string       `json:"guid"`
	Home        string       `json:"home"`
	LimaHome    string       `json:"lima_home"`
	Instance    string       `json:"instance"`
	Port        uint16       `json:"port"`
	LimaRef     macFileState `json:"lima_ref"`
	VMRef       macFileState `json:"vm_ref"`
	LimaBinary  macFileState `json:"lima_binary"`
	ProfileHash string       `json:"profile_hash"`
}

func (e *macExecutor) publishMacGuard(ctx context.Context, owner *macAccount) error {
	if err := e.checkMacProfile(ctx, owner); err != nil {
		return err
	}
	_, binary, err := macReadFile(e.limaBinary(), 128<<20)
	if err != nil {
		return err
	}
	profile, _, err := macReadFile(filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "profile.yaml"), 256<<10)
	if err != nil {
		return err
	}
	policy := macGuardPolicy{Version: 1, Account: e.ledger.Account, Owner: owner.Name, UID: owner.UID, GID: owner.GID, GUID: owner.GUID, Home: owner.Home, LimaHome: e.ledger.LimaHome, Instance: e.ledger.Instance, Port: e.ledger.GuestPort, LimaRef: e.ledger.LimaRef, VMRef: e.ledger.VMRef, LimaBinary: binary, ProfileHash: macDigest(profile)}
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return e.installFile("guard-policy", filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "guard.json"), append(data, '\n'), 0444)
}

// RunDarwinVMGuard is only the fixed launchd child entry, under the original
// VM owner's native uid. Caller-provided policy/path/runtime values cannot
// construct it. Root's original immutable policy confines every restart.
func RunDarwinVMGuard(ctx context.Context, account string) error {
	if !code.ValidAccount(account) {
		return macFailure("join_mac_guard", "the Mac startup account is invalid")
	}
	data, _, err := macReadFile(filepath.Join(filepath.Dir(macHelperPath(account)), "guard.json"), 32<<10)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var policy macGuardPolicy
	if decoder.Decode(&policy) != nil || decoder.Decode(new(any)) != io.EOF || policy.Version != 1 || policy.Account != account || policy.Instance != account+"-host" || policy.UID <= 0 || policy.GID <= 0 || policy.Port == 0 || os.Getuid() != policy.UID || os.Geteuid() != policy.UID {
		return macFailure("join_mac_guard", "this native launch does not own the original Mac VM policy")
	}
	owner, err := macReadAccount(ctx, policy.Owner)
	if err != nil {
		return err
	}
	if owner.UID != policy.UID || owner.GID != policy.GID || owner.GUID != policy.GUID || owner.Home != policy.Home {
		return macFailure("join_mac_guard", "the original Mac VM owner changed")
	}
	current, err := macDirectoryIdentity(policy.LimaHome, owner.UID)
	if err != nil || current != policy.LimaRef {
		return macFailure("join_mac_guard", "the original Lima home was replaced")
	}
	current, err = macDirectoryIdentity(filepath.Join(policy.LimaHome, policy.Instance), owner.UID)
	if err != nil || current != policy.VMRef {
		return macFailure("join_mac_guard", "the original Mac VM directory was replaced")
	}
	profile, err := macBoundedUserProfile(filepath.Join(policy.LimaHome, policy.Instance, "lima.yaml"))
	if err != nil {
		return err
	}
	if macDigest(profile) != policy.ProfileHash {
		return macFailure("join_mac_guard", "the original closed Lima profile changed before restart")
	}
	for _, name := range []string{"default.yaml", "override.yaml"} {
		if _, err := os.Lstat(filepath.Join(policy.LimaHome, "_config", name)); !errors.Is(err, os.ErrNotExist) {
			return macFailure("join_mac_guard", "an applicable Lima default or override would change the closed VM")
		}
	}
	binary := filepath.Join(filepath.Dir(macHelperPath(account)), "runtime/lima/bin/limactl")
	_, actual, err := macReadFile(binary, 128<<20)
	if err != nil || actual != policy.LimaBinary {
		return macFailure("join_mac_guard", "the original pinned Lima execution object changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	environment := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "HOME=" + owner.Home, "LIMA_HOME=" + policy.LimaHome}
	if err := syscall.Exec(binary, []string{binary, "start", policy.Instance, "--foreground"}, environment); err != nil {
		return fmt.Errorf("execute the original guarded Lima startup: %w", err)
	}
	return nil
}

func macBoundedUserProfile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return nil, macFailure("join_mac_profile", "the original Lima profile is unavailable or too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, 256<<10+1))
	if err != nil || len(data) > 256<<10 {
		return nil, macFailure("join_mac_profile", "the original Lima profile exceeds its bound")
	}
	return data, nil
}
