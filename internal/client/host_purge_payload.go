package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/supply"
)

// A transient mapped/self-checked cleanup command is never installed, added
// to current, bootstrapped or admitted to ordinary bridge/session dispatch.
func (p *purgeOperationV1) runPurgePayload(ctx context.Context, action string, witness *api.PurgeGuardWitness) ([]byte, error) {
	if action != "report" && action != "commit" {
		return nil, grantOperationError()
	}
	if err := p.check(ctx, p.conn); err != nil {
		return nil, err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if action == "commit" && !record.readyForGuard() {
		return nil, grantOperationError()
	}
	arch := record.Report.Arch
	if arch == "" {
		// An interrupted request may not have delivered an architecture. Probe is
		// a fixed read-only command under the SAME native cleanup route.
		raw, err := p.runFixedNative(ctx, "uname -m", nil, 128)
		if err != nil {
			return nil, err
		}
		switch strings.TrimSpace(string(raw)) {
		case "x86_64":
			arch = "amd64"
		case "aarch64", "arm64":
			arch = "arm64"
		default:
			return nil, hostStateRemovalError("the cleanup route returned an unknown host architecture")
		}
	}
	helper, ok := helpers.For("linux", arch)
	if !ok {
		return nil, &UserError{Msg: "this client has no carried helper for the fixed cleanup command", Hint: "Install a complete current client, then resume the same host rm --purge command.", Code: "no_helper"}
	}
	manifest, err := ReleaseManifest()
	if err != nil {
		return nil, err
	}
	item, ok := manifest.Find(supply.KindHelper, brand.HostBinary, arch)
	if !ok {
		return nil, grantOperationError()
	}
	if err := supply.Check(item, brand.HostBinary, helper); err != nil {
		return nil, err
	}
	nonce, err := grantRequestNonce()
	if err != nil {
		return nil, err
	}
	plan := api.PurgeCleanupV1{Version: 1, HostID: record.HostID, Nonce: record.Nonce, Action: action, Witness: witness, Install: api.InstallPlanV1{Version: 1, Nonce: nonce, HelperVersion: HelperVersion(), Arch: arch, Items: []api.InstallItemV1{{Kind: item.Kind, Name: item.Name, Size: item.Size, SHA256: item.SHA256}}}}
	data, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	dir := `"$HOME/.` + brand.Name + `-cleanup-` + nonce + `"`
	sum := sha256.Sum256(helper)
	stage := strings.Join([]string{"set -eu", "umask 077", "d=" + dir, `mkdir "$d"`, `t="$d/` + brand.HostBinary + `"`, `set -C`, `cat > "$t"`, fmt.Sprintf(`echo "%s  $t" | sha256sum -c --quiet -`, hex.EncodeToString(sum[:])), `chmod 755 "$t"`, `echo __PURGE_STAGED__`}, "; ")
	output, err := p.runFixedNative(ctx, stage, bytes.NewReader(helper), 4096)
	if err != nil {
		return nil, err
	}
	if string(output) != "__PURGE_STAGED__\n" {
		return nil, hostStateRemovalError("the original transient cleanup staging did not finish")
	}
	return p.runFixedNative(ctx, `exec `+dir+`/`+brand.HostBinary+` purge-payload`, bytes.NewReader(data), 4<<20)
}

func (p *purgeOperationV1) commitNativeGuard(ctx context.Context, w api.PurgeGuardWitness) error {
	raw, err := p.runPurgePayload(ctx, "commit", &w)
	if err != nil {
		return err
	}
	if string(raw) != "__PURGE_COMMITTED__\n" {
		return hostStateRemovalError("the original fixed cleanup command did not acknowledge completion")
	}
	p.mu.Lock()
	p.receipt.Report.GuardRemoved = true
	p.mu.Unlock()
	return p.finish(ctx, "guard")
}
