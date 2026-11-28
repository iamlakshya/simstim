#!/usr/bin/env python3
"""Escape verification suite run inside a process-wall sandbox (isolation spec 4.1).

Exits 0 only if every isolation invariant holds:
- no access to host home or host runtime sockets
- no visibility of host processes in /proc
- no writes outside the workspace, /home/agent and /tmp
- filtered /etc (no /etc/environment, /etc/shadow, /etc/ssh; uid 1000 agent)
- no /dev/kvm, /dev/fuse or /dev/net/tun
- zero Linux capabilities and NoNewPrivs=1
- seccomp blocks unshare, mount, bpf, io_uring_setup, clone3 (ENOSYS),
  AF_VSOCK and NETLINK_AUDIT, while allowing NETLINK_ROUTE read-only
- no direct outbound TCP or UDP outside loopback
"""

import ctypes
import ctypes.util
import errno
import os
import platform
import socket
import struct
import sys

FAILURES = []


def check(name, cond, detail=""):
    if cond:
        print(f"PASS: {name}")
    else:
        msg = f"FAIL: {name}" + (f" ({detail})" if detail else "")
        print(msg, file=sys.stderr)
        FAILURES.append(msg)


def get_syscall_numbers():
    arch = platform.machine()
    if arch in ("x86_64", "amd64"):
        return {
            "unshare": 272,
            "mount": 165,
            "bpf": 321,
            "io_uring_setup": 425,
            "clone3": 435,
        }
    if arch in ("aarch64", "arm64"):
        return {
            "unshare": 97,
            "mount": 40,
            "bpf": 280,
            "io_uring_setup": 425,
            "clone3": 435,
        }
    return {}


def main():
    host_home = os.environ.get("ESCAPE_HOST_HOME", "")
    host_canary = os.environ.get("ESCAPE_HOST_CANARY", "")
    host_pid = os.environ.get("ESCAPE_HOST_PID", "")
    host_runtime = os.environ.get("ESCAPE_HOST_RUNTIME", "")
    workspace = os.environ.get("ESCAPE_WORKSPACE", os.getcwd())

    # 1. Identity & capabilities.
    check("uid-1000", os.getuid() == 1000 and os.getgid() == 1000, f"uid={os.getuid()} gid={os.getgid()}")
    status = {}
    with open("/proc/self/status", "r", encoding="utf-8") as f:
        for line in f:
            if ":" in line:
                k, v = line.split(":", 1)
                status[k.strip()] = v.strip()
    check("cap-eff-zero", status.get("CapEff") == "0000000000000000", status.get("CapEff", ""))
    check("cap-prm-zero", status.get("CapPrm") == "0000000000000000", status.get("CapPrm", ""))
    check("cap-inh-zero", status.get("CapInh") == "0000000000000000", status.get("CapInh", ""))
    check("no-new-privs", status.get("NoNewPrivs") == "1", status.get("NoNewPrivs", ""))

    # 2. No host home / canary / runtime dir.
    if host_home and host_home != "/home/agent":
        check("no-host-home", not os.path.exists(host_home), f"{host_home} exists")
    if host_canary:
        check("no-host-canary", not os.path.exists(host_canary), f"{host_canary} exists")
    if host_runtime:
        check("no-host-runtime", not os.path.exists(host_runtime), f"{host_runtime} exists")
    check("no-run-user", not os.path.exists("/run/user"), "/run/user exists")

    # 3. PID namespace: /proc shows only sandbox processes.
    pids = [p for p in os.listdir("/proc") if p.isdigit()]
    check("pid-ns-small", len(pids) <= 16, f"saw {len(pids)} pids: {pids}")
    if host_pid:
        check("no-host-pid", host_pid not in pids, f"host pid {host_pid} visible in /proc")

    # 4. Filtered /etc and synthetic passwd.
    for blocked in ("/etc/environment", "/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/ssh"):
        check(f"etc-blocked:{blocked}", not os.path.exists(blocked), f"{blocked} exists")
    with open("/etc/passwd", "r", encoding="utf-8") as f:
        passwd = f.read()
    check("etc-passwd-agent", "agent:x:1000:1000:" in passwd and len(passwd.strip().splitlines()) <= 3, passwd.strip())

    # 5. No dangerous devices.
    for dev in ("/dev/kvm", "/dev/fuse", "/dev/net/tun", "/dev/vhost-vsock"):
        check(f"no-device:{dev}", not os.path.exists(dev), f"{dev} exists")

    # 6. No writes outside workspace, /home/agent, /tmp.
    for target in (
        "/escape-root-test",
        "/etc/escape-test",
        "/usr/escape-test",
        "/work/escape-parent-test",
        os.path.join("/home", "escape-parent-test"),
        "/run/escape-parent-test",
    ):
        try:
            with open(target, "w", encoding="utf-8") as f:
                f.write("bad")
            os.unlink(target)
            check(f"no-write:{target}", False, "write unexpectedly succeeded")
        except OSError as exc:
            check(f"no-write:{target}", exc.errno in (errno.EACCES, errno.EROFS, errno.EPERM, errno.ENOENT), f"errno={exc.errno}")

    # 7. Writes allowed in workspace, private home and /tmp.
    for label, d in (("workspace", workspace), ("home", "/home/agent"), ("tmp", "/tmp")):
        p = os.path.join(d, f".escape-ok-{os.getpid()}")
        try:
            with open(p, "w", encoding="utf-8") as f:
                f.write("ok")
            os.unlink(p)
            check(f"write-ok:{label}", True)
        except OSError as exc:
            check(f"write-ok:{label}", False, str(exc))

    # 8. Seccomp checks via libc syscall().
    libc = ctypes.CDLL(ctypes.util.find_library("c"), use_errno=True)
    nrs = get_syscall_numbers()
    if nrs:
        CLONE_NEWUSER = 0x10000000
        rc = libc.syscall(nrs["unshare"], CLONE_NEWUSER)
        err = ctypes.get_errno()
        check("seccomp-unshare-eperm", rc == -1 and err == errno.EPERM, f"rc={rc} errno={err}")

        rc = libc.syscall(nrs["mount"], b"none", b"/tmp", b"tmpfs", 0, None)
        err = ctypes.get_errno()
        check("seccomp-mount-eperm", rc == -1 and err == errno.EPERM, f"rc={rc} errno={err}")

        rc = libc.syscall(nrs["bpf"], 0, None, 0)
        err = ctypes.get_errno()
        check("seccomp-bpf-eperm", rc == -1 and err == errno.EPERM, f"rc={rc} errno={err}")

        rc = libc.syscall(nrs["io_uring_setup"], 1, None)
        err = ctypes.get_errno()
        check("seccomp-io-uring-eperm", rc == -1 and err == errno.EPERM, f"rc={rc} errno={err}")

        rc = libc.syscall(nrs["clone3"], None, 0)
        err = ctypes.get_errno()
        check("seccomp-clone3-enosys", rc == -1 and err == errno.ENOSYS, f"rc={rc} errno={err}")

    # 9. Socket families & Netlink.
    AF_VSOCK = 40
    try:
        s = socket.socket(AF_VSOCK, socket.SOCK_STREAM)
        s.close()
        check("seccomp-af-vsock-eperm", False, "AF_VSOCK socket unexpectedly opened")
    except OSError as exc:
        check("seccomp-af-vsock-eperm", exc.errno == errno.EPERM, f"errno={exc.errno}")

    NETLINK_AUDIT = 9
    try:
        s = socket.socket(socket.AF_NETLINK, socket.SOCK_DGRAM, NETLINK_AUDIT)
        s.close()
        check("seccomp-netlink-audit-eperm", False, "NETLINK_AUDIT socket unexpectedly opened")
    except OSError as exc:
        check("seccomp-netlink-audit-eperm", exc.errno == errno.EPERM, f"errno={exc.errno}")

    # NETLINK_ROUTE opens (for getifaddrs / os.networkInterfaces()), but RTM_NEWADDR fails with EPERM.
    NETLINK_ROUTE = 0
    try:
        s = socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, NETLINK_ROUTE)
        s.bind((0, 0))
        # Build RTM_NEWADDR (type=20, flags=NLM_F_REQUEST|NLM_F_ACK=0x5).
        RTM_NEWADDR = 20
        NLM_F_REQUEST = 0x1
        NLM_F_ACK = 0x4
        ifaddrmsg = struct.pack("=BBBB I", socket.AF_INET, 32, 0, 0, 1)
        nlmsg = struct.pack("=IHHII", 16 + len(ifaddrmsg), RTM_NEWADDR, NLM_F_REQUEST | NLM_F_ACK, 1, 0) + ifaddrmsg
        s.send(nlmsg)
        resp = s.recv(4096)
        s.close()
        # NLMSG_ERROR (type=2) carries negated errno at offset 16.
        _, mtype, _, _, _ = struct.unpack_from("=IHHII", resp, 0)
        nl_err = -struct.unpack_from("=i", resp, 16)[0] if mtype == 2 and len(resp) >= 20 else 0
        check("netlink-route-rtm-newaddr-eperm", mtype == 2 and nl_err == errno.EPERM, f"type={mtype} err={nl_err}")
    except OSError as exc:
        check("netlink-route-rtm-newaddr-eperm", False, str(exc))

    # 10. Direct outbound TCP and UDP fail closed (only lo exists in network namespace).
    ifaces = sorted(os.listdir("/sys/class/net")) if os.path.exists("/sys/class/net") else []
    check("netns-lo-only", not ifaces or ifaces == ["lo"], f"ifaces={ifaces}")

    tcp_blocked = False
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(1.0)
        s.connect(("1.1.1.1", 443))
        s.close()
    except OSError as exc:
        tcp_blocked = exc.errno in (errno.ENETUNREACH, errno.EHOSTUNREACH, errno.EACCES, errno.EPERM)
    check("no-direct-tcp", tcp_blocked, "outbound TCP unexpectedly connected")

    udp_blocked = False
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.settimeout(1.0)
        s.sendto(b"\x00", ("1.1.1.1", 53))
        s.close()
    except OSError as exc:
        udp_blocked = exc.errno in (errno.ENETUNREACH, errno.EHOSTUNREACH, errno.EACCES, errno.EPERM)
    check("no-direct-udp", udp_blocked, "outbound UDP unexpectedly sent")

    if FAILURES:
        print(f"escape suite: {len(FAILURES)} check(s) failed", file=sys.stderr)
        sys.exit(1)
    print("escape suite: all checks passed")


if __name__ == "__main__":
    main()
