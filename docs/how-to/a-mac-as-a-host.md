# Use a Mac as a host

A joined Mac hosts a Linux VM. The laptop connects through the Mac's existing
Remote Login service, using a key limited to that VM and one fixed facts action.
The normal Linux helper runs inside the VM. No native Mac session helper runs
on the Mac.

Use Apple silicon with macOS 15 or later. M3 and later chips can give the VM
nested virtualisation and microvm walls. M1 and M2 machines keep the Linux VM
and can use process or gvisor walls. The actual guest probe determines the wall;
a chip name alone never promises KVM.

Ask the organisation's IT team before joining a managed Mac. MDM administrators
can read the VM and its agent logins. The reply suggests the shared tier when
MDM enrolment is present or cannot be checked. Review the repository and secret
tier rules on the laptop before accepting that host.

## Join from the Mac's keyboard

On the laptop, run `simstim host invite mac-host`. Copy the public invite through
a channel you trust. On the Mac, install the signed native client and run
`simstim host join`. Paste the invite at its prompt. An invite on a pipe needs
`--yes`; the code is never a command-line argument.

The review describes a hidden standard account, its disabled SecureToken, the
pinned Lima release and Linux image, the narrow key, sshd rules, startup and undo.
The account gets a random password that is never retained. It is not a
FileVault user. The client pins Lima in a root-owned folder, so a Homebrew
upgrade cannot replace the running installation.

Remote Login must already be on. The join never enables it or edits the list
of users allowed to use it. If the dedicated account is not allowed, add that
account in System Settings > General > Sharing > Remote Login, then run the
join again. Keep the root-owned leave script after any partial failure.

The VM uses plain mode, with no home mounts, port forwards or containerd.
`networkOpts.hostLoopback` is false. Its SSH loopback port stays fixed. The
laptop's key cannot open a Mac shell, a terminal, an agent or X11 channel, a
remote forward or a Unix-socket forward. It can reach only the VM's port and
run `facts-v1` through the root-owned dispatcher.

The join checks sshd's effective configuration. Earlier MDM rules can override
a drop-in. A mismatch stops the join and retains its original undo journal.
Apple's socket-activated sshd reads the checked file for new connections; the
join leaves Remote Login under the owner's control.

Accept the reply on the laptop with `simstim host add mac-host --accept -`.
Compare every printed host-key fingerprint, including the Mac and its VM.
The laptop still starts every connection.

## Keep it available

Automatic CPU and memory sizes stay below half the Mac's totals. `--cpus` and
`--memory` choose other explicit sizes below that limit. Lima does not return
all freed VM memory to macOS, so leave room for the Mac's own work.

The default startup job is a system launch daemon. It includes the dedicated
account, `LIMA_HOME`, restart-on-error behaviour and 60 seconds for shutdown.
A root-owned guard checks the original VM directory, pinned Lima and frozen
closed profile before each restart. A changed default, override or profile
stops startup.

The never-logged-in service-account case still needs the manual checks below.
If that case fails on your macOS release, undo the partial join and choose
`--start-mode user-agent --agent-user me` for an existing Mac user. That user
must stay logged in. Anyone at its unlocked screen can read the VM disk and
agent logins. The hidden account still holds the narrow outer key. The client
and doctor report this change of custody.

Choose `--ac-awake` only after reviewing its AC-only power change. It sets
`pmset -c sleep 0` and records the old value for leave. The join never changes
battery policy or uses the undocumented `disablesleep` setting.

Keep the Mac on AC power, keep its lid open and lock its screen. Set Charge
Limit to 80% yourself. Use a ventilated place at 10–35 °C. A closed lid without
an external display can make the Mac sleep despite its AC sleep setting.

After a power cut, restart or macOS update, log in locally to unlock FileVault.
The tool never unlocks it or reboots the Mac. The startup job then starts the
VM, and the normal Linux helper can restore eligible sessions. Install macOS
updates by hand or at the times your organisation sets.

`host doctor` reports the Mac's power, sleep and startup warnings. On battery,
automatic placement ranks it one band lower. Unreadable observations remain
unknown and do not change the Linux guest's memory or admission facts.

## Leave from the Mac

Run `sudo simstim host leave` at the Mac's keyboard and review its undo.
The original root-owned helper stops its startup job and VM, removes only its
anchored files and original account, and restores the recorded AC sleep value.
A changed file, account identity or uncertain VM stops cleanup and preserves
the ledger for inspection. Battery settings remain untouched.

`host rm --purge` on the laptop removes the helper's resources in the Linux VM.
Its narrow Mac key cannot undo Mac setup. Run `sudo simstim host leave` on the
Mac to remove the outer account and VM.

## Required manual assurance

This implementation has not proved the hardware cases below. Use an
owner-approved M1 or M2 Mac and an M3 or later Mac, first on macOS 15 and then
on the newest macOS. Log every change outside the repository. Ask the owner
before any reboot.

1. Join with a fresh hidden account. Never log in as that account.
2. Verify its SecureToken is disabled and it has no login keychain.
3. While another user is logged in, reach the VM from the laptop and check the
   expected process or microvm wall through the normal Linux probe.
4. Log out the other user and repeat the connection test.
5. With the owner's approval, reboot, unlock FileVault locally and test again.
6. Test the explicit user launch-agent branch, including its warning, login
   dependency and automatic restart after that user logs in.
7. Check that ordinary shell, TTY, agent, X11, remote and Unix forwards fail,
   while only the exact VM channel and `facts-v1` work.
8. Check facts from the standard account on AC and battery, then run leave and
   compare the original AC settings and all recorded native objects.

Do not treat parser, plan or protocol fixtures as proof of these Mac behaviours.
