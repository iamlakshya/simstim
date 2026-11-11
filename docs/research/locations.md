# Hosts in two locations

Researched on 10 November 2026.

A user may keep hosts in more than one location: for example a homelab, and Mac laptops in another building where no other machine stays online. This note gathers the facts for that setup: Mac laptops as hosts, homelab hosts, and ways to reach both locations. It also covers a way to add a machine from its own keyboard. The design that follows is in [the spec](../contributing/isolation-spec.md), section 18, and in the plan's tasks H19 to H21.

The rules for the note:

- Facts come from vendor docs, man pages and release notes. Each one links its source. Man pages have no Apple URL, so a researcher read them on a Mac laptop with macOS 26.5.1. The note marks those facts "man page".
- **UNVERIFIED** marks a claim that no primary source confirmed.
- No host name, address or account of a real user appears here.

## Summary

1. **The invoker picks the host, and rules fill in.** Hosts are known by their names, and labels exist today as metadata. Nothing yet picks a host for a repository, an organisation or a kind of task. `sandbox.host` in a profile or a repository section adds that (spec 18.2). Tools for one person work the same way (section 7).
2. **A Mac laptop can be a host, through a Lima VM, with limits.** It must stay on power with the lid open. After an unplanned restart, FileVault holds it at the unlock screen until someone unlocks it at the Mac, or over SSH from its LAN (section 5). Tailscale starts only after that unlock. With no other machine online at that location, nothing can wake or unlock it from far away. So the design assumes that a Mac host does not restart, and that its user logs in after a rare restart (spec 18.4).
3. **A machine can prepare itself, and the laptop still connects to it.** `host join` on the machine makes a dedicated account, the VM and a narrow SSH path, then prints a reply code. The laptop reads the code and connects over SSH as `host add` does today. The machine never calls the laptop or any service of ours. The codes hold no secret, but must arrive unchanged (section 6).
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

1. **The invoker picks, and rules fill in.** A profile, a repository or an organisation can name the host for its sessions. No rule picks another host instead of the one that the invoker names. The grant gate can still stop a start. A secret can list the only hosts that may get it.
2. **A Mac host is a Lima VM behind a narrow door.** The VM runs in plain mode, with `hostLoopback` off and a fixed SSH port. A dedicated macOS account holds a key that can only open a channel to that port. sshd's drop-in also stops remote forwards. A launch daemon, which the join writes itself, starts the VM at boot (section 5).
3. **The Mac stays awake on purpose.** The join sets `pmset -c sleep 0` with the user's consent and records the old values. The docs say: lid open, on power, Charge Limit at 80 %, good air flow.
4. **The design assumes no restarts.** On 11 November 2026 the owner set this: a Mac host does not restart, and after a rare restart its user logs in at the Mac. The tool has no unlock from far away, and `ls` marks a Mac host "away" until it answers (spec 18.4).
5. **The doctor reads MDM control.** It reports enrolment, a managed sshd, hidden settings and an Energy Saver payload, and suggests the tier `shared` on a Mac that an organisation manages.
6. **No second machine at a Mac's location.** Nothing in the design needs a waker, a jump host or a subnet router there.
7. **In the homelab, the docs recommend a VM and the probe decides.** The doctor names the fix for a missing `/dev/kvm`: CPU type `host` on Proxmox.
8. **Hosts that stay on need no wake.** For a host that sleeps, the user can write a provider script that sends the wake from a machine on its own LAN. The examples have no such script. The tool listens nowhere.

## 5. Mac hosts, checked again

Checked on 10 November 2026, after an audit of the spec. A researcher read the man pages on a Mac laptop with macOS 26.5.1.

- **A VM at boot, with nobody logged in.** Lima's contributors report that a `vz` Linux VM starts from `limactl autostart --condition=boot` after a reboot, with nobody logged in. They tested Apple silicon with macOS 26 ([pull 4984](https://github.com/lima-vm/lima/pull/4984), [pull 5088](https://github.com/lima-vm/lima/pull/5088)). A user report shows a hidden service account that ran a `vz` VM from a launch daemon ([issue 5087](https://github.com/lima-vm/lima/issues/5087)).
- **The keychain.** The keychain errors in Apple's forums come from macOS guests ([thread 822670](https://developer.apple.com/forums/thread/822670)). No report links them to Linux guests.
- **No support from Apple.** Apple's support staff say that the Virtualization framework "links with AppKit, which is obviously not daemon-safe" ([thread 841688](https://developer.apple.com/forums/thread/841688)). So a macOS update can break a start at boot.
- **UNVERIFIED:** a start from a launch daemon for an account that has never logged in and has no login keychain, while another user is logged in. The test at the end of this section settles it.
- **`limactl` and root.** `limactl autostart enable --condition=boot --user <user>` exists from Lima v2.2.0. It writes `/Library/LaunchDaemons/io.lima-vm.daemon.<instance>.plist` with `UserName`. `KeepAlive` and `LIMA_HOME` are in the plist from v2.3.0 ([autostart](https://lima-vm.io/docs/usage/autostart/), [plist](https://github.com/lima-vm/lima/blob/v2.3.0/pkg/autostart/launchd/io.lima-vm.daemon.INSTANCE.plist)). `limactl` refuses to run as root, and runs `sudo` itself for the install ([source](https://github.com/lima-vm/lima/blob/v2.3.0/cmd/limactl/autostart_darwin.go)). An account with no known password cannot answer that `sudo`.
- **The daemon's limits.** `KeepAlive` restarts the job only after an exit with an error. Without `ExitTimeOut`, launchd kills the host agent 5 s after `SIGTERM`, before the guest shuts down ([issue 5765](https://github.com/lima-vm/lima/issues/5765)). The label holds only the instance name, so two instances with one name collide ([issue 5643](https://github.com/lima-vm/lima/issues/5643)).
- **macOS versions.** `vz` needs macOS 13 ([vz](https://lima-vm.io/docs/config/vmtype/vz/)). On 9 November 2026, Apple released security fixes for macOS 27, 26 and 15 only. The last fix for macOS 14 came on 6 August 2026 ([security releases](https://support.apple.com/en-us/100100)). So macOS 15 is the oldest release that still gets security fixes.
- **The FileVault unlock over SSH** takes the password of a FileVault user, not a key. SSH then drops, and the Mac finishes the boot (man page `fdesetup`). Before the unlock, no daemon on the data volume runs, Tailscale included. Any machine that reaches the Mac's LAN address can do the unlock, for example through a Tailscale subnet router on that LAN ([subnet routers](https://tailscale.com/kb/1019/subnets)). The design no longer needs this unlock, because a Mac host's user logs in after a restart (spec 18.4).
- **Commands without root.** `pmset -g batt`, `pmset -g assertions`, `fdesetup status` and `profiles status -type enrollment` ran without `sudo` for an admin user. **UNVERIFIED** for a standard account. `pmset -c sleep 0` needs root (man page `pmset`). On Apple silicon, `pmset -g therm` reported only that no thermal warning was recorded.
- **Remote Login without root.** `launchctl print-disabled system` shows `"com.openssh.sshd" => enabled` when Remote Login is on. A security benchmark uses this check without `sudo` ([DISA STIG check](https://stigaview.com/products/macos14/v2r1/APPL-14-000080), secondary).
- **The lid.** With the lid closed and no display, a Mac laptop sleeps, whatever `pmset -c sleep` says ([Apple](https://support.apple.com/en-us/102501)). `pmset disablesleep 1` is undocumented, and only third parties report that it works.
- **sshd and the key's options.** macOS 26.5.1 ships `Include /etc/ssh/sshd_config.d/*`, with OpenSSH 10.2p1. sshd runs a forced `command=` through the account's login shell (man page `sshd`). `permitopen` limits local forwards to a literal host and port, so the client must ask for `127.0.0.1`, not `localhost`.
- **Remote forwards.** The key's options do not limit remote forwards. With no `permitlisten`, the key can listen on any TCP port. It can listen on any Unix socket path even with `permitlisten` ([channels.c](https://github.com/openssh/openssh-portable/blob/V_10_2_P1/channels.c)). Only sshd's config stops these, with `AllowTcpForwarding local` and `AllowStreamLocalForwarding no`.
- **Hidden accounts.** launchd runs a system job as its `UserName`, and needs no password (man page `launchd.plist`). When a user logs in, macOS tries to give that user a secure token. `;DisabledTags;SecureToken` in the account's `AuthenticationAuthority` stops this, so the account never becomes a FileVault user ([secure tokens](https://support.apple.com/guide/deployment/use-secure-and-bootstrap-tokens-dep24dbdcf9e/web)). Security tools flag hidden accounts ([MITRE ATT&CK T1564.002](https://attack.mitre.org/techniques/T1564/002/)).
- **Local network privacy** allows any daemon that launchd starts, so the VM reaches the LAN with no prompt ([TN3179](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy)).
- **Login Items.** From macOS 13, a user can turn a launch daemon off in System Settings > General > Login Items & Extensions ([Apple](https://support.apple.com/guide/deployment/depdca572563/web)).

**A test by hand, before H21.** Do it on Apple silicon with FileVault on, first with macOS 15, then with macOS 26:

1. Make a hidden standard account with a random password. Do not log in as it.
2. As that account, make a `vz` Linux VM with Lima v2.3.0.
3. As root, write the daemon's plist with `UserName` set to the account.
4. As root, run `launchctl bootstrap system <plist>`, while your own user stays logged in.
5. From a second machine, connect through the Mac's sshd to the VM's port.
6. Log out of your own user.
7. Do step 5 again.
8. Restart the Mac, and log in as your own user at the login window.
9. Do step 5 again.
10. Record `limactl list`, the daemon's error log, and `log show --last 10m --predicate 'process == "limactl"'`. Look for errors about keys or the keychain.

The test passes when the VM answers in steps 5, 7 and 9, and the account has no login keychain.

## 6. Joining a machine

Checked on 10 November 2026.

| Tool | What the code carries | Lifetime | Typos and theft | How the user enters it |
| --- | --- | --- | --- | --- |
| kubeadm | A token `[a-z0-9]{6}.[a-z0-9]{16}`. `--discovery-token-ca-cert-hash` pins the CA. | 24 hours, `--ttl` | No check characters. A thief can join nodes. | An argument or a config file |
| k3s | `K10<CA hash>::<credentials>` | 24 hours for bootstrap tokens | The server token gives "full administrator access". | `--token`, `K3S_TOKEN` or `--token-file` |
| Tailscale | An auth key | 1 to 90 days | One-off keys end after use. Reusable keys are "very dangerous if stolen". | `--auth-key`, or `file:<path>` |
| Teleport | A secret token, and `--ca-pin` | 30 minutes by default | Secret tokens are "prone to secret exfiltration". `bound_keypair` registers a public key first. | `--token`, a value or a file |
| Syncthing | A device ID: the SHA-256 of a certificate, in base32 | No end | One check character in each group. The ID is public, and both sides add each other. | A paste |
| Magic Wormhole | A number and words, 16 bits, with SPAKE2 | One use | An attacker gets one guess. | A prompt |
| Proxmox | An address, a certificate fingerprint and the root password | None | The password is typed by hand. | `pvecm add` prompts |

Sources: [bootstrap tokens](https://kubernetes.io/docs/reference/access-authn-authz/bootstrap-tokens/), [kubeadm token](https://kubernetes.io/docs/reference/setup-tools/kubeadm/kubeadm-token/), [kubeadm join](https://kubernetes.io/docs/reference/setup-tools/kubeadm/kubeadm-join/), [k3s token](https://docs.k3s.io/cli/token), [Tailscale auth keys](https://tailscale.com/kb/1085/auth-keys), [Teleport join methods](https://goteleport.com/docs/reference/deployment/join-methods/), [Syncthing device IDs](https://docs.syncthing.net/dev/device-ids.html), [Magic Wormhole](https://magic-wormhole.readthedocs.io/en/latest/welcome.html), [pvecm](https://pve.proxmox.com/pve-docs/pvecm.1.html).

- **The nearest models.** Syncthing is the nearest: its IDs are public, and the user carries them both ways. Teleport's `bound_keypair` is the nearest server model, because it registers a public key before the join.
- **The lifetime.** One hour is in line with Teleport, which advises a token "valid only 1 hour for example". kubeadm and k3s use 24 hours. A Mac join downloads Lima and an image, so it can take more than one hour. kubeadm and Teleport let the user set the lifetime.
- **Typos.** A 40-bit hash of the whole code misses a random error about once in 10^12, but it cannot say where the error is. Syncthing puts a check character in each group for this reason ([luhn.go](https://github.com/syncthing/syncthing/blob/main/lib/protocol/luhn.go)). The codes of this design pass 100 characters, so users will paste them.
- **A leaked code.** A leaked invite gives nothing to log in with: the key is public, and the nonce only matches a reply. A leaked reply shows addresses and facts, but no secret.
- **A changed code.** A changed invite makes the machine trust another key. A forged reply makes the laptop pin other host keys and send its work there. Anyone who saw the invite can forge a reply. A 40-bit check takes about 10^12 SHA-256 trials to match, which one current GPU does in about a minute ([hashcat figures](https://gist.github.com/Chick3nman/32e662a5bb63bc4f51b847bb422222fd), secondary). ZRTP can use a short check only because a hash commitment gives the attacker one guess ([RFC 6189](https://www.rfc-editor.org/rfc/rfc6189.html), section 4.4.1.1). So the check is a typo check. The defence is a channel that the user trusts, and a comparison of host key fingerprints.
- **Standard input.** By default every user can read a process's command line ([proc(5)](https://man7.org/linux/man-pages/man5/proc.5.html)), and Bash writes typed lines to a file ([history](https://www.gnu.org/software/bash/manual/html_node/Bash-History-Facilities.html)). Docker and gh read secrets from standard input for this reason ([docker login](https://docs.docker.com/reference/cli/docker/login/), [gh auth login](https://cli.github.com/manual/gh_auth_login)).
- **`restrict` and forwards.** `restrict` turns off forwarding, the PTY and `~/.ssh/rc`. `pty` and `port-forwarding` turn the first two back on ([sshd(8)](https://man.openbsd.org/sshd.8)). `port-forwarding` allows all four kinds of forward: local and remote TCP, and local and remote Unix sockets. `AllowTcpForwarding local` stops remote TCP forwards. `AllowStreamLocalForwarding local` stops a remote Unix-socket forward and keeps local ones, and `no` stops both ([sshd_config(5)](https://man.openbsd.org/sshd_config.5), [auth-options.c](https://github.com/openssh/openssh-portable/blob/master/auth-options.c), [channels.c](https://github.com/openssh/openssh-portable/blob/master/channels.c)).
- **Drop-ins.** `Include` came in OpenSSH 8.2 ([release notes](https://www.openssh.com/txt/release-8.2)). Debian 11 and Ubuntu 20.04 include `/etc/ssh/sshd_config.d/`. Debian 10 does not. sshd reads included files in lexical order, and the first value of a keyword wins, also in `Match` blocks ([sshd_config(5)](https://man.openbsd.org/sshd_config.5)). `sshd -T -C user=<account>` prints the settings that apply to an account.
- **Linger.** `loginctl enable-linger` starts a user's service manager at boot, and keeps it after logout ([loginctl](https://man7.org/linux/man-pages/man1/loginctl.1.html)).

## 7. Picking a host

Checked on 10 November 2026.

| Tool | Does an explicit choice win? | A default by repository or organisation? | Do labels select? |
| --- | --- | --- | --- |
| GitHub Actions | Yes, within runner groups | Organisations scope runner groups to repositories | Yes: a runner needs all the labels |
| GitLab | Yes | Untagged jobs go only to runners set to take them | Yes: all the tags |
| Buildkite | Yes | A step with no queue goes to the default queue | Yes |
| Codespaces | Within policy | `hostRequirements` for each repository, policy for each organisation | No |
| Coder | Within template permissions | Templates belong to an organisation | No |
| DevPod | Yes, `--provider` | One default provider | No |
| Docker | Yes, `--context` | No | No |
| kubectl | Yes, `--context` | No | No |

Sources: [runners in a workflow](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/use-in-a-workflow), [GitLab runners](https://docs.gitlab.com/ci/runners/configure_runners/), [Buildkite queues](https://buildkite.com/docs/agent/queues), [Codespaces minimum specification](https://docs.github.com/en/codespaces/setting-up-your-project-for-codespaces/configuring-dev-containers/setting-a-minimum-specification-for-codespace-machines), [Coder templates](https://coder.com/docs/admin/templates/template-permissions), [DevPod providers](https://devpod.sh/docs/managing-providers/add-provider), [Docker contexts](https://docs.docker.com/engine/manage-resources/contexts/), [kubeconfig](https://kubernetes.io/docs/concepts/configuration/organize-cluster-access-kubeconfig/).

- **Two families.** Tools for one person name a target, the person's flag wins, and one default applies. CI systems use labels as selectors, because any worker of a pool can take a job. Hosted platforms add defaults and limits for each repository or organisation. **UNVERIFIED:** whether Coder or DevPod have a default for each repository.
- **The longest match.** Config keyed by a URL usually takes the longest match. Git matches `http.<url>` "on a slash (`/`) boundary", and "longer matches take precedence". Its `insteadOf` uses "the longest match" ([git-config](https://git-scm.com/docs/git-config)). nginx takes "the longest matching prefix" ([nginx](https://nginx.org/en/docs/http/ngx_http_core_module.html)). ssh_config takes the first match, so its users must order sections by hand ([ssh_config](https://man.openbsd.org/ssh_config.5)). TOML promises no order for the keys of a table ([TOML 1.0](https://toml.io/en/v1.0.0)), so an order rule cannot work for `[repos.*]` sections.
- **Git's own repository match.** `includeIf "hasconfig:remote.*.url:<pattern>"` came in Git 2.36 ([release notes](https://raw.githubusercontent.com/git/git/master/Documentation/RelNotes/2.36.0.adoc)). It matches any remote, not only `origin`, and the last value wins.
- **Refusals.** Shared platforms refuse some choices. Codespaces limits machine types by an organisation's policy ([machine types](https://docs.github.com/en/codespaces/managing-codespaces-for-your-organization/restricting-access-to-machine-types)). Docker, kubectl and DevPod never refuse a flag.

**What the design takes.** The design follows the tools for one person, and adds defaults for each repository, as hosted platforms do. A rule never picks another host instead of the invoker's. The grant gate can still stop a start, and says why. A section name that ends in `/*` matches on a `/` boundary, as Git's `http.<url>` does, and only the `origin` remote counts. Labels stay metadata, because `new auto` already covers pools of hosts.
