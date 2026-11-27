package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

type installCarriedV1 struct {
	name string
	data []byte
}

func installOwnedHelperV1(ctx context.Context, transport Transport, platform Platform, files []installCarriedV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, err := ReleaseManifest()
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	plan := api.InstallPlanV1{Version: 1, Nonce: hex.EncodeToString(random[:]), HelperVersion: HelperVersion(), Arch: platform.Arch}
	var helper []byte
	var bodies []io.Reader
	for _, file := range files {
		if file.data == nil {
			continue
		}
		kind := ""
		switch file.name {
		case brand.HostBinary:
			kind = supply.KindHelper
			helper = file.data
		case brand.VTBinary:
			kind = supply.KindVT
		case brand.UsernsBinary:
			kind = supply.KindUserns
		}
		item, found := manifest.Find(kind, file.name, platform.Arch)
		if !found || kind == "" {
			return &UserError{Msg: "this client has no checked carried file for the installation", Hint: "Install a release of this client or rebuild it with just build.", Code: "manifest_damaged"}
		}
		if err := supply.Check(item, file.name, file.data); err != nil {
			return err
		}
		plan.Items = append(plan.Items, api.InstallItemV1{Kind: kind, Name: file.name, Size: item.Size, SHA256: item.SHA256})
		bodies = append(bodies, bytes.NewReader(file.data))
	}
	if helper == nil {
		return &UserError{Msg: "this client carries no host helper", Hint: "Install a complete release of the client.", Code: "no_helper"}
	}
	header, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(header) > 16<<10 {
		return &UserError{Msg: "the carried helper plan exceeds its fixed bound", Hint: "Rebuild the client and its carried helpers together.", Code: "manifest_damaged"}
	}
	sum := sha256.Sum256(helper)
	dir := `"$HOME/.` + brand.Name + `-install-` + plan.Nonce + `"`
	stage := strings.Join([]string{"set -eu", "umask 077", "d=" + dir, `mkdir "$d"`, `t="$d/` + brand.HostBinary + `"`, `cat > "$t"`, fmt.Sprintf(`echo "%s  $t" | sha256sum -c --quiet -`, hex.EncodeToString(sum[:])), `chmod 755 "$t"`, `echo __STAGED__`}, "; ")
	out, err := transport.Run(ctx, stage, bytes.NewReader(helper))
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "__STAGED__") {
		return &UserError{Msg: "the original helper staging did not finish", Hint: "Keep the host saved and check its free disk space before retrying.", Code: "install_failed"}
	}
	input := []io.Reader{strings.NewReader(fmt.Sprintf("%08x\n", len(header))), bytes.NewReader(header)}
	input = append(input, bodies...)
	command := `exec ` + dir + `/` + brand.HostBinary + ` install-payload`
	out, err = transport.Run(ctx, command, io.MultiReader(input...))
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "__INSTALLED__") {
		return &UserError{Msg: "the original native helper installation did not finish", Hint: "Keep the host state and staging files for inspection before retrying.", Code: "install_failed"}
	}
	return nil
}
