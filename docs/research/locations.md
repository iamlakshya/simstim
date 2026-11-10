# Hosts in two locations

Researched on 10 November 2026.

A user may keep hosts in more than one location: for example a homelab, and Mac laptops in another building where no other machine stays online. This note gathers the facts for that setup: Mac laptops as hosts, homelab hosts, and ways to reach both locations. It also covers a way to add a machine from its own keyboard. The design that follows is in [the spec](../contributing/isolation-spec.md), section 18, and in the plan's tasks H19 to H21.

The rules for the note:

- Facts come from vendor docs, man pages and release notes. Each one links its source. Man pages have no Apple URL, so a researcher read them on a Mac laptop with macOS 26.5.1. The note marks those facts "man page".
- **UNVERIFIED** marks a claim that no primary source confirmed.
- No host name, address or account of a real user appears here.

## Summary

1. **A location is a label and a rule.** Labels exist today: `host add --label location=annex` saves them. A rule that keeps a repository, a profile or a secret on the hosts of one location does not exist. The new key `on_hosts` adds it (spec 18.2).
2. **A Mac laptop can be a host, through a Lima VM, with limits.** It must stay on power with the lid open. After an unplanned restart, FileVault holds it at the unlock screen until someone unlocks it from the Mac's own LAN. Tailscale starts only after that unlock. With no other machine online at that location, nothing can wake or unlock it from far away.
3. **A machine can prepare itself, and the laptop still connects to it.** `host join` on the machine makes a dedicated account, the VM and a narrow SSH path, then prints a reply code. The laptop reads the code and connects over SSH as `host add` does today. The machine never calls the laptop or any service of ours.
4. **In a homelab, use a VM with the host's CPU type, and probe for KVM.** Proxmox hides the CPU's virtualisation flags from a VM by default, and an LXC container shares the Proxmox kernel. TrueNAS and Unraid hosts get the helper in a VM.
5. **Each host joins the user's own overlay.** Tailscale runs on each host as a system daemon. The tool adds no relay. Wake-on-LAN cannot cross Tailscale, so a sleeping host needs an awake machine on its own LAN.

## 1. Mac laptops as hosts

### 1.1 Keep the Mac awake

- `pmset -c sleep 0` turns off system sleep on the charger. `-c` means the charger, and `0` means never. The settings persist in `/Library/Preferences/SystemConfiguration/com.apple.PowerManagement.plist` (man page `pmset`).
- `pmset -g cap` on a Mac laptop lists `sleep`, `womp`, `ttyskeepawake`, `standby`, `powernap`, `tcpkeepalive`, `lowpowermode` and `highpowermode`. It does not list `autorestart`, so a Mac laptop cannot restart itself after a power loss through `pmset` (man page).
- `disablesleep` is not in the `pmset` man page on macOS 26.5.1. UNVERIFIED: no Apple document describes it.
- `caffeinate -s` blocks system sleep only while the Mac is on power, and only while `caffeinate` runs (man page `caffeinate`).
- The Battery settings have "Prevent automatic sleeping on power adapter when the display is off" and "Wake for network access" ([Battery settings](https://support.apple.com/guide/mac-help/mchlfc3b7879/mac)). Apple describes the wake only for access to shared resources. No Apple source says that SSH or Tailscale traffic wakes a sleeping Mac laptop.
- Apple documents use with the lid closed only with an external display, a keyboard and a mouse, on power ([external display](https://support.apple.com/guide/macbook-pro/connect-an-external-display-apd8cdd74f57/mac), [closed-lid mode](https://support.apple.com/en-la/102501)). UNVERIFIED: use with the lid closed and no display. Only third-party tools offer it.
- An Apple silicon laptop turns on when its lid opens or when power connects. On macOS 15 and later, `nvram BootPreference` changes this ([power-on](https://support.apple.com/en-us/120622)).
- Charge Limit needs macOS 26.4 or later on Apple silicon. It holds the battery at 80 % to 100 %, and charges to 100 % now and then to keep its estimates right ([battery charging](https://support.apple.com/en-us/102338)).
- Apple says to use a laptop at 10 °C to 35 °C, on a stable surface with good ventilation ([temperature](https://support.apple.com/en-us/102336)).

### 1.2 Restarts and FileVault

- When FileVault is on, automatic login is off ([automatic login](https://support.apple.com/en-us/102316)).
- On Apple silicon with macOS 26 or later, an SSH session can unlock FileVault after a restart. Remote Login must be on, and a network must be available ([FileVault](https://support.apple.com/guide/security/managing-filevault-sec8447f5049/web), [deployment](https://support.apple.com/guide/deployment/dep82064ec40/web)).
- Before the unlock, the network cannot use a VPN, a network relay or 802.1X. Apple states this for Platform SSO ([Platform SSO](https://support.apple.com/guide/deployment/platform-sso-for-mac-dep7bbb05313/web)). This note infers the same limit for the SSH unlock. So Tailscale cannot carry the unlock, and Wi-Fi with 802.1X may not connect.
- `fdesetup authrestart` restarts the Mac and skips the first unlock. It keeps an unlock key in memory and in the SMC for that restart, so FileVault protects less during it. The man page says that it "may not work on all systems".
- A launchd agent runs only while its user is logged in. A daemon runs for the whole system ([launchd jobs](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)).

### 1.3 Remote Login, sshd and MDM

- Remote Login is in System Settings > General > Sharing. "Allow access for" can name only some users ([Remote Login](https://support.apple.com/guide/mac-help/mchlp1066/mac)).
- `systemsetup -setremotelogin on` needs Full Disk Access (man page `systemsetup`).
- `/etc/ssh/sshd_config` includes `/etc/ssh/sshd_config.d/*`, and macOS ships `100-macos.conf` there with `UsePAM yes`. A drop-in file in that folder can harden sshd.
- MDM can replace sshd's files with `ServicesConfigurationFiles` for `com.apple.sshd`. sshd then ignores the files in `/etc/ssh` ([ServicesConfigurationFiles](https://developer.apple.com/documentation/devicemanagement/servicesconfigurationfiles)).
- MDM can hide the Sharing and Battery settings with `DisabledSystemSettings` ([system preferences profile](https://github.com/apple/device-management/blob/release/mdm/profiles/com.apple.systempreferences.yaml)). The Energy Saver payload sets sleep timers and can destroy the FileVault key on standby ([Energy Saver](https://support.apple.com/guide/deployment/depe8b3e427/web)).
- UNVERIFIED: an MDM key that turns Remote Login off directly.

### 1.4 Lima in November 2026

- The current release is v2.3.0, of 6 November 2026. It adds `networkOpts.hostLoopback` for `vz`, checks the guest's SSH host key, and makes Ubuntu 26.10 the default image ([v2.3.0](https://github.com/lima-vm/lima/releases/tag/v2.3.0)).
- `vz` has been the default driver on macOS since v1.0, and needs macOS 13 or later ([vz](https://lima-vm.io/docs/config/vmtype/vz/)). Nested virtualisation needs `vz`, an M3 chip or later, and macOS 15 or later ([template](https://github.com/lima-vm/lima/blob/v2.3.0/templates/default.yaml), [Apple](https://developer.apple.com/documentation/virtualization/vzgenericplatformconfiguration/isnestedvirtualizationsupported)).
- `limactl autostart enable --condition=boot --user <user>` installs `/Library/LaunchDaemons/io.lima-vm.daemon.<instance>.plist`. The VM then starts before any user logs in, as that user. `--keep-alive` is on by default ([autostart](https://lima-vm.io/docs/usage/autostart/)).
- The default template mounts the home folder read-only ([mounts](https://github.com/lima-vm/lima/blob/v2.3.0/templates/_default/mounts.yaml)). A read-only mount still shows `~/.ssh` to the guest.
- `hostLoopback` is true by default. False stops the guest from reaching the Mac's loopback services. The guest can still reach the Mac's LAN address. Lima's advice is "Set this to false for a guest that runs untrusted code."
- In plain mode (`--plain`), Lima makes no mounts and forwards only `static: true` ports ([ports](https://lima-vm.io/docs/config/port/)).
- `ssh.localPort` is 0 by default, which picks a random port on `127.0.0.1`. `limactl create` has no flag for it, so `--set '.ssh.localPort=N'` sets it ([flags](https://github.com/lima-vm/lima/blob/v2.3.0/cmd/limactl/editflags/editflags.go)). With `vz` and systemd 256 or later, SSH uses vsock, but the host agent still listens on that loopback port ([forwarder](https://github.com/lima-vm/lima/blob/v2.3.0/pkg/driver/vz/vsock_forwarder.go)).
- The VM trusts only Lima's own key, `~/.lima/_config/user.pub`. `loadDotSSHPubKeys` has been false since v1.0.
- By default a VM gets at most 4 CPUs and 4 GiB of memory. Virtualization.framework returns only part of a guest's freed memory to macOS ([overview](https://github.com/apple/container/blob/main/docs/technical-overview.md)).

### 1.5 Apple `container`

- `container` 1.7.1 came out on 6 November 2026, under Apache-2.0. It runs one small VM for each container ([container](https://github.com/apple/container)). It needs Apple silicon and macOS 26.
- Its API server is a launch agent. A user report of June 2026 says it fails for a user without a login session, when a launch daemon starts it ([issue 205](https://github.com/apple/container/issues/205)). A host that nobody logs in to needs exactly that case.
- It runs on macOS only, so the helper in a Linux VM cannot drive it. This plan does not use it.

## 2. Homelab hosts

### 2.1 Proxmox VE

- The current release is 9.2, of 21 May 2026, with kernel 7.0 and QEMU 11.0 ([roadmap](https://pve.proxmox.com/wiki/Roadmap)).
- Proxmox does not show the CPU's virtualisation flags to a VM by default. `qm set <vmid> --cpu host` shows them. Since 9.1, the `nested-virt` flag does too, on a CPU model that matches the host ([nested](https://pve.proxmox.com/wiki/Nested_Virtualization), [qm](https://pve.proxmox.com/pve-docs/chapter-qm.html)). A VM with those flags cannot live-migrate.
- UNVERIFIED: whether nested virtualisation is on by default in the Proxmox kernel. Upstream Linux has turned it on since 4.20 ([kernel](https://docs.kernel.org/virt/kvm/x86/running-nested-guests.html)). `/sys/module/kvm_intel/parameters/nested` or `kvm_amd` shows it.
- New LXC containers are unprivileged. `nesting` is off by default, and turning it on shows the host's procfs and sysfs to the container. Proxmox says that a VM isolates better, because containers share the host's kernel ([containers](https://pve.proxmox.com/pve-docs/chapter-pct.html)).

### 2.2 Other bases

- Debian's kernels allow unprivileged user namespaces ([bubblewrap](https://packages.debian.org/trixie/bubblewrap)).
- Ubuntu 24.04 and later restrict them with `AppArmor`. A profile with a `userns,` rule allows one program, and a sysctl turns the restriction off for all ([24.04](https://documentation.ubuntu.com/release-notes/24.04/)). The spec's root step already installs such a profile (14.4.2).
- Incus VMs get the virtualisation flags by default, and its containers do not ([options](https://linuxcontainers.org/incus/docs/main/reference/instance_options/)).
- TrueNAS does not support changes from the command line, and they may not survive an upgrade ([TrueNAS 25.10](https://www.truenas.com/docs/scale/25.10/scaleuireference/containersscreens/)).
- Unraid runs VMs with KVM, and "Host passthrough" shows the CPU's features to the VM ([Unraid](https://docs.unraid.net/unraid-os/using-unraid-to/create-virtual-machines/vm-setup/)).

### 2.3 Wake-on-LAN

- The firmware and the network card must support it. `ethtool -s <if> wol g` turns on the magic packet ([ethtool](https://man7.org/linux/man-pages/man8/ethtool.8.html)), and `WakeOnLan=magic` in a systemd `.link` file sets it at each boot ([systemd.link](https://www.freedesktop.org/software/systemd/man/latest/systemd.link.html)).
- Proxmox wakes a node with `pvenode wakeonlan <node>` ([pvenode](https://pve.proxmox.com/pve-docs/pvenode.1.html)).
- Routers must block directed broadcasts by default ([RFC 2644](https://www.rfc-editor.org/rfc/rfc2644)), so the packet must start on the target's own LAN. Tailscale cannot carry it, even through a subnet router. Tailscale's own advice is an always-on LAN device that sends the packet ([wake-on-LAN](https://tailscale.com/blog/wake-on-lan-tailscale-upsnap)).

## 3. Reaching each location

- On macOS, only the open-source `tailscaled` runs before login and can be a Tailscale SSH server. The App Store and standalone apps start at login ([variants](https://tailscale.com/kb/1065/macos-variants)). `sudo tailscaled install-system-daemon` installs it as a launch daemon ([macOS](https://github.com/tailscale/tailscale/wiki/Tailscaled-on-macOS)).
- Tailscale needs no inbound port in most networks. It uses outbound HTTPS and UDP ([firewall ports](https://tailscale.com/kb/1082/firewall-ports)). A managed network may block it. UNVERIFIED for any one network.
- A new tailnet's policy allows all traffic, and so does a policy with no `acls` section ([policy](https://tailscale.com/kb/1018/acls)). Tags and grants narrow it.
- WireGuard answers no packet that fails authentication ([paper](https://www.wireguard.com/papers/wireguard.pdf), section 5.1).
- `ProxyJump` connects to the jump host, then opens a TCP channel through it to the target ([ssh_config](https://man.openbsd.org/ssh_config)).

## 4. What the design takes from this

1. **Labels name locations, and `on_hosts` keeps work there.** A repository, a profile or a secret names the hosts or labels that may get it. `new` refuses any other host.
2. **A Mac host is a Lima VM behind a narrow door.** The VM runs in plain mode, with `hostLoopback` off and a fixed SSH port. A dedicated macOS account holds a key that can only open a channel to that port. A launch daemon starts the VM at boot.
3. **The Mac stays awake on purpose.** The join sets `pmset -c sleep 0` with the user's consent and records the old values. The docs say: lid open, on power, Charge Limit at 80 %, good air flow.
4. **Restarts are a known gap.** The docs explain the FileVault unlock and `fdesetup authrestart`. `host doctor` shows the restart risk, and `ls` marks a Mac host "away" until it answers.
5. **The doctor reads MDM control.** It reports enrolment, a managed sshd, hidden settings and an Energy Saver payload, and suggests the tier `shared` on a Mac that an organisation manages.
6. **No second machine at a Mac's location.** Nothing in the design needs a waker, a jump host or a subnet router there.
7. **In the homelab, the docs recommend a VM and the probe decides.** The doctor names the fix for a missing `/dev/kvm`: CPU type `host` on Proxmox.
8. **Wake a homelab host through a provider script on its own LAN.** The tool's existing provider scripts run the wake. The tool listens nowhere.
