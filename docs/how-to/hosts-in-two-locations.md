# Hosts in two locations

Use your own Tailscale on the laptop and each host. Use its tags and grants
to control which devices may reach SSH. When the laptop is on the same LAN,
Tailscale can connect directly. The same saved address can work at either
location.

[Add each host](add-a-host.md), then use a name or a
[host rule](pick-a-host.md). A location label is a note that you can see in
the host list and launcher. It does not choose where a session runs.

```sh
simstim host set box label.location=home
simstim host set other label.location=office
```

## Virtual machines in a homelab

On Proxmox, give the VM the host's CPU type, or a matching CPU model with
nested virtualisation enabled. This lets the probe see hardware
virtualisation when the host can provide it. The host CPU type ties the VM
to those CPU features. Do not rely on live migration.

If the VM has no `/dev/kvm` and cannot see the processor's virtualisation
flags, `host doctor` suggests exposing the host CPU. On Proxmox, the command
is `qm set <vmid> --cpu host`. Run it as the machine's owner through your
normal administration process.

Prefer a VM to an LXC container. A container shares the host kernel. The
sandbox probe decides which walls a container can provide. Do not make a
container privileged or pass `/dev/kvm` into one to follow this guide.

On TrueNAS or Unraid, run the helper inside a VM. Keep the base system
untouched. TrueNAS does not support changes through its command line.

## Wake and routes

Homelab hosts normally stay on. A provider script can wake a sleeping host
when you have a machine on the same LAN to send the wake packet. Wake-on-LAN
packets do not cross Tailscale. The example provider scripts start cloud
machines and are not LAN wake services.

The client still starts every SSH connection. These settings add no relay,
listening port or host callback to the laptop.
