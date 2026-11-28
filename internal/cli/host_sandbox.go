package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// pinHostFloorWithProbe probes the host during host add / host create, prints
// the Sandboxes block to w, and pins h.WallFloor to the strongest level that
// probes OK unless h.WallFloor already requested a lower level.
func pinHostFloorWithProbe(ctx context.Context, w io.Writer, conn *client.Conn, h *client.Host, releaseURL string) (api.CapProbe, error) {
	if !conn.HasFeature("sandbox") {
		return api.CapProbe{}, nil
	}
	p, err := conn.CapProbe(ctx, releaseURL)
	if err != nil {
		return api.CapProbe{}, err
	}
	nowName, _ := api.LevelsFor(p, api.RuntimeStatus{})
	best, _ := wall.ParseLevel(nowName)
	if h.WallFloor != "" {
		want, perr := wall.ParseLevel(h.WallFloor)
		if perr != nil {
			return api.CapProbe{}, &client.UserError{
				Msg:  fmt.Sprintf("--floor: %v", perr),
				Hint: "Pass none, process, gvisor or microvm.",
				Code: "bad_value",
				Kind: client.KindUsage,
			}
		}
		if want > best {
			fmt.Fprintln(w)
			printSandboxesBlock(w, w, h.Name, p, "")
			return api.CapProbe{}, &client.UserError{
				Msg:  fmt.Sprintf("%s can run up to %s, so --floor cannot be %s", h.Name, best, want),
				Hint: fmt.Sprintf("Pass --floor %s, or omit --floor to pin %s.", best, best),
				Code: "wall_floor",
				Kind: client.KindRefused,
			}
		}
		h.WallFloor = want.String()
	} else {
		h.WallFloor = best.String()
	}
	fmt.Fprintln(w)
	printSandboxesBlock(w, w, h.Name, p, h.WallFloor)
	return p, nil
}

// printSandboxesBlock prints the "Sandboxes on <host>" summary of isolation
// spec 14.2.1. Status rows go to rows (stdout in doctor/show, stderr in add),
// and Try: hints go to hints (stderr).
func printSandboxesBlock(rows, hints io.Writer, hostName string, p api.CapProbe, floor string) {
	p = client.CleanCapProbe(p)
	fmt.Fprintf(rows, "Sandboxes on %s:\n", hostName)
	printSystemSummaryLine(rows, p)
	printKVMLines(rows, hints, p)
	printMicroVMLine(rows, p)
	printGVisorLine(rows, hints, p)
	printProcessWallLines(rows, hints, p)
	printDiskLine(rows, p)
	now, afterRoot := api.LevelsFor(p, api.RuntimeStatus{})
	levelLine := "  Level now: " + now
	if floor != "" {
		levelLine += " (floor: " + floor + ")"
	}
	levelLine += "."
	if afterRoot != now {
		levelLine += " After setup: " + afterRoot + "."
	}
	fmt.Fprintln(rows, levelLine)
}

func printSystemSummaryLine(rows io.Writer, p api.CapProbe) {
	var parts []string
	if p.Kernel != "" {
		parts = append(parts, "kernel "+p.Kernel)
	}
	if p.Systemd > 0 {
		parts = append(parts, fmt.Sprintf("systemd %d", p.Systemd))
	}
	if p.CgroupV2 {
		if len(p.Delegated) > 0 {
			parts = append(parts, "cgroup v2 with "+strings.Join(p.Delegated, ", "))
		} else {
			parts = append(parts, "cgroup v2")
		}
	}
	if len(parts) > 0 {
		fmt.Fprintf(rows, "  ✓ %s\n", strings.Join(parts, ", "))
	}
}

func printKVMLines(rows, hints io.Writer, p api.CapProbe) {
	if !p.KVM.Present {
		detail := "no /dev/kvm"
		if p.Virt != "" {
			detail += ": this " + p.Virt + " guest has no nested virtualisation"
		}
		fmt.Fprintf(rows, "  ✕ %s\n", detail)
		return
	}
	virtLabel := ""
	switch p.CPUVirt {
	case "vmx":
		virtLabel = ", Intel VT-x"
	case "svm":
		virtLabel = ", AMD-V"
	}
	fmt.Fprintf(rows, "  ✓ /dev/kvm present%s\n", virtLabel)
	if !p.KVM.Usable {
		mode := p.KVM.Mode
		if mode == "" {
			mode = "restricted"
		}
		group := p.KVM.Group
		if group == "" {
			group = "kvm"
		}
		user := p.User
		if user == "" {
			user = "$USER"
		}
		fmt.Fprintf(rows, "  ✕ /dev/kvm not open to %s (%s root %s)\n", user, mode, group)
		qGroup := client.ShellQuote(group)
		qUser := `"$USER"`
		qACL := `u:"$USER":rw`
		if p.User != "" {
			qUser = client.ShellQuote(p.User)
			if qUser != p.User || strings.ContainsAny(p.User, ":/") {
				qACL = client.ShellQuote("u:" + p.User + ":rw")
			} else {
				qACL = "u:" + p.User + ":rw"
			}
		}
		if p.HasSetfacl {
			fmt.Fprintf(hints, "    Try: sudo setfacl -m %s /dev/kvm && sudo usermod -aG %s %s\n", qACL, qGroup, qUser)
		} else {
			fmt.Fprintf(hints, "    Try: sudo usermod -aG %s %s (then reboot or restart your login session)\n", qGroup, qUser)
		}
	}
}

func printMicroVMLine(rows io.Writer, p api.CapProbe) {
	for _, w := range p.Walls {
		if w.Level != api.LevelMicroVM || !w.OK {
			continue
		}
		detail := "Cloud Hypervisor"
		if w.Driver != "" && w.Driver != "chv" {
			detail = w.Driver
		}
		if w.Version != "" {
			detail += " " + w.Version
		}
		fmt.Fprintf(rows, "  ✓ %s\n", detail)
		return
	}
}

func printGVisorLine(rows, hints io.Writer, p api.CapProbe) {
	for _, w := range p.Walls {
		if w.Level != api.LevelGVisor {
			continue
		}
		if w.OK {
			detail := "gVisor"
			if w.Version != "" {
				detail += " " + w.Version
			}
			if plat := w.Facts["platform"]; plat != "" {
				detail += " (" + plat + ")"
			}
			fmt.Fprintf(rows, "  ✓ %s\n", detail)
			return
		}
		if w.Facts["runsc"] != "" {
			reason := w.Reason
			if reason == "" {
				reason = "not ready"
			}
			fmt.Fprintf(rows, "  ✕ gVisor: %s\n", reason)
			if w.Fix != "" {
				fmt.Fprintf(hints, "    Try: %s\n", w.Fix)
			}
		}
		return
	}
}

func printProcessWallLines(rows, hints io.Writer, p api.CapProbe) {
	if p.OS != "" && p.OS != "linux" {
		fmt.Fprintf(rows, "  ✕ process walls require Linux\n")
		fmt.Fprintf(hints, "    Try: use a Linux host, or run inside a Lima VM on macOS\n")
		return
	}
	var proc *api.WallProbe
	for i := range p.Walls {
		if p.Walls[i].Level == api.LevelProcess {
			proc = &p.Walls[i]
			break
		}
	}
	if p.Bwrap {
		ver := p.BwrapVersion
		if ver == "" {
			ver = "installed"
		}
		fmt.Fprintf(rows, "  ✓ bubblewrap %s\n", ver)
	} else {
		fmt.Fprintf(rows, "  ✕ bubblewrap (bwrap) is not installed\n")
		fix := "install the bubblewrap package"
		if proc != nil && proc.Fix != "" && strings.Contains(proc.Reason, "bubblewrap") {
			fix = proc.Fix
		}
		fmt.Fprintf(hints, "    Try: %s\n", fix)
	}

	var okParts []string
	if p.LandlockABI >= 1 {
		okParts = append(okParts, fmt.Sprintf("Landlock ABI %d", p.LandlockABI))
	}
	if p.Seccomp {
		okParts = append(okParts, "seccomp")
	}
	if p.UserNS {
		if p.UserNSRestricted {
			okParts = append(okParts, "user namespaces (AppArmor profile active)")
		} else {
			okParts = append(okParts, "user namespaces")
		}
	}
	if len(okParts) > 0 {
		fmt.Fprintf(rows, "  ✓ %s\n", strings.Join(okParts, ", "))
	}

	if kernelTooOld(p.Kernel) {
		fmt.Fprintf(rows, "  ✕ kernel %s is older than Linux 5.13\n", p.Kernel)
		fmt.Fprintf(hints, "    Try: upgrade the host kernel to Linux 5.13 or later\n")
	}
	if p.LandlockABI < 1 {
		fmt.Fprintf(rows, "  ✕ Landlock is not enabled on this kernel\n")
		fmt.Fprintf(hints, "    Try: boot a Linux 5.13 or later kernel with Landlock enabled\n")
	}
	if !p.Seccomp {
		fmt.Fprintf(rows, "  ✕ seccomp filters are not available\n")
		fmt.Fprintf(hints, "    Try: run on a Linux host with CONFIG_SECCOMP_FILTER enabled\n")
	}
	printUserNSLine(rows, hints, p, proc)
}

func kernelTooOld(release string) bool {
	maj, minVal, ok := api.ParseKernelMajorMinor(release)
	return ok && (maj < 5 || (maj == 5 && minVal < 13))
}

func procFailedOnUserNS(p api.CapProbe, proc *api.WallProbe) bool {
	return proc != nil && !proc.OK && p.Bwrap && p.LandlockABI >= 1 && p.Seccomp && !kernelTooOld(p.Kernel)
}

func printUserNSLine(rows, hints io.Writer, p api.CapProbe, proc *api.WallProbe) {
	if !p.UserNS {
		if p.UserNSRestricted {
			fmt.Fprintf(rows, "  ✕ user namespaces are restricted by AppArmor (kernel.apparmor_restrict_unprivileged_userns = 1)\n")
			if procFailedOnUserNS(p, proc) && proc.Fix != "" {
				fmt.Fprintf(hints, "    Try: %s\n", proc.Fix)
			} else if p.Bwrap {
				fmt.Fprintf(hints, "    Try: %s\n", process.AppArmorFallbackCmd())
			}
			return
		}
		fmt.Fprintf(rows, "  ✕ unprivileged user namespaces are not available\n")
		if procFailedOnUserNS(p, proc) && proc.Fix != "" {
			fmt.Fprintf(hints, "    Try: %s\n", proc.Fix)
		} else {
			fmt.Fprintf(hints, "    Try: enable unprivileged user namespaces on this host\n")
		}
		return
	}
	if p.Bwrap && proc != nil && !proc.OK {
		alreadyShown := (kernelTooOld(p.Kernel) && strings.Contains(proc.Reason, "5.13")) ||
			(p.LandlockABI < 1 && strings.Contains(proc.Reason, "Landlock")) ||
			(!p.Seccomp && strings.Contains(proc.Reason, "seccomp"))
		if !alreadyShown {
			fmt.Fprintf(rows, "  ✕ %s\n", proc.Reason)
			if proc.Fix != "" {
				fmt.Fprintf(hints, "    Try: %s\n", proc.Fix)
			}
		}
	}
}

func printDiskLine(rows io.Writer, p api.CapProbe) {
	if p.DiskFree <= 0 {
		return
	}
	detail := fmt.Sprintf("%.1f GiB free in ~/.local/share/%s", float64(p.DiskFree)/(1<<30), brand.Name)
	if p.DiskFS != "" {
		detail += ", " + p.DiskFS
		if p.Reflink {
			detail += " with reflink"
		}
	}
	fmt.Fprintf(rows, "  ✓ %s\n", detail)
}

func printWallDrivers(rows, hints io.Writer, walls []api.WallProbe) {
	for _, w := range client.CleanWallProbes(walls) {
		mark := "✓"
		detail := "ok"
		if w.Version != "" {
			detail += " (" + w.Version + ")"
		}
		if !w.OK {
			mark = "✕"
			detail = w.Reason
		}
		fmt.Fprintf(rows, "  %s %-10s %-8s %s\n", mark, w.Driver, w.Level, detail)
		if !w.OK && w.Fix != "" {
			fmt.Fprintf(hints, "    Try: %s\n", w.Fix)
		}
	}
}
