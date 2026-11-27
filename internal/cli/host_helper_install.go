package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func ensureHostHelper(ctx context.Context, w io.Writer, hostName string, s client.Transport, p client.Platform, consent func(q string) (bool, error), log *onboard.Log) error {
	if p.Installed {
		fmt.Fprintf(w, "  ✓ %s %s is already installed\n", brand.HostBinary, client.HelperVersion())
		return nil
	}
	bin, ok := helpers.For(p.OS, p.Arch)
	if !ok {
		return &client.UserError{Msg: fmt.Sprintf("no helper for %s/%s in this build", p.OS, p.Arch), Hint: "Use a Linux host on amd64 or arm64.", Code: "no_helper"}
	}
	fmt.Fprintf(w, "\n%s needs a small helper on the host to keep sessions alive.\n", brand.DisplayName)
	fmt.Fprintf(w, "    What   %s %s, %.1f MB, SHA-256 checked\n", brand.HostBinary, client.HelperVersion(), float64(len(bin))/1e6)
	fmt.Fprintf(w, "    Where  ~/.local/share/%s/bin/%s/\n", brand.Name, client.HelperVersion())
	fmt.Fprintf(w, "    Also   keep sessions running after you log out, and install this\n")
	fmt.Fprintf(w, "           terminal's terminfo\n")
	fmt.Fprintf(w, "  Nothing listens on the network. To remove it later, delete ~/.local/share/%s on the host.\n\n", brand.Name)
	ok, err := consent("Install it?")
	if err != nil {
		return err
	}
	if !ok {
		return &client.UserError{Msg: "cancelled; nothing was installed", Hint: brand.Name + " host add " + hostName, Code: "cancelled", Kind: client.KindCancelled}
	}
	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err = client.Install(ictx, s, p)
	cancel()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  ✓ installed %s %s\n", brand.HostBinary, client.HelperVersion())
	log.Add("installed the helper on %s", hostName)
	return nil
}
