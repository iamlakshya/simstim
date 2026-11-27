#!/usr/bin/env bash
# Checks a guest kernel .config against isolation spec 20.1 and the "Generally
# Necessary" symbols of moby's contrib/check-config.sh (pinned in
# guest/supply.lock).
#
#   guest/kernel/check-config.sh <config> <arch>
set -euo pipefail

usage() {
    echo "check-config: $1" >&2
    echo "Try: guest/kernel/check-config.sh guest/kernel/config-amd64 amd64" >&2
    exit 1
}

if [ "$#" -ne 2 ]; then
    usage "needs a .config path and an architecture (amd64 or arm64)."
fi

config=$1
arch=$2

case "$arch" in
    amd64 | arm64) ;;
    *) usage "unknown architecture \"${arch}\": use amd64 or arm64." ;;
esac

if [ ! -f "$config" ] || [ ! -r "$config" ]; then
    echo "check-config: cannot read ${config}." >&2
    echo "Try: pass a readable .config file, such as guest/kernel/config-${arch}" >&2
    exit 1
fi

# Required =y symbols from spec 20.1, plus every "Generally Necessary" symbol
# of moby/moby contrib/check-config.sh at commit 70efc0e31da3 (Linux 6.18).
required=(
    EFI
    EFI_STUB
    ACPI
    PCI
    VIRTIO_PCI
    VIRTIO_MMIO
    VIRTIO_BLK
    VIRTIO_CONSOLE
    HW_RANDOM_VIRTIO
    VIRTIO_BALLOON
    VSOCKETS
    VIRTIO_VSOCKETS
    BLK_DEV_DM
    DM_INIT
    DM_VERITY
    EROFS_FS
    EXT4_FS
    OVERLAY_FS
    TMPFS
    DEVTMPFS
    NF_TABLES
    NFT_REDIR
    CGROUPS
    MEMCG
    SECURITY_LANDLOCK
    SECCOMP_FILTER
    USER_NS
    FUSE_FS
    VMGENID
    RANDSTRUCT_NONE
    DEBUG_INFO_NONE
    NAMESPACES
    NET_NS
    PID_NS
    IPC_NS
    UTS_NS
    CGROUP_CPUACCT
    CGROUP_DEVICE
    CGROUP_FREEZER
    CGROUP_SCHED
    CPUSETS
    KEYS
    VETH
    BRIDGE
    BRIDGE_NETFILTER
    IP_NF_FILTER
    IP_NF_MANGLE
    IP_NF_TARGET_MASQUERADE
    IP6_NF_FILTER
    IP6_NF_MANGLE
    IP6_NF_TARGET_MASQUERADE
    NETFILTER_XT_MATCH_ADDRTYPE
    NETFILTER_XT_MATCH_CONNTRACK
    NETFILTER_XT_MATCH_IPVS
    NETFILTER_XT_MARK
    IP_NF_RAW
    IP_NF_NAT
    NF_NAT
    IP6_NF_RAW
    IP6_NF_NAT
    POSIX_MQUEUE
    CGROUP_BPF
)

if [ "$arch" = "amd64" ]; then
    required+=(PVH)
fi

forbidden=(
    MODULES
    IO_URING
    VSOCKETS_LOOPBACK
    KVM
    IKHEADERS
    LOCALVERSION_AUTO
)

req_list="${required[*]}"
forb_list="${forbidden[*]}"

awk -v cfg="$config" -v req_str="$req_list" -v forb_str="$forb_list" '
/^# CONFIG_[A-Za-z0-9_]+ is not set$/ {
    sym = $2
    sub(/^CONFIG_/, "", sym)
    val[sym] = "n"
    next
}
/^CONFIG_[A-Za-z0-9_]+=/ {
    eq = index($0, "=")
    sym = substr($0, 8, eq - 8)
    val[sym] = substr($0, eq + 1)
    next
}
END {
    bad = 0
    nreq = split(req_str, req, " ")
    for (i = 1; i <= nreq; i++) {
        s = req[i]
        v = (s in val) ? val[s] : "n"
        if (v != "y") {
            printf "check-config: CONFIG_%s is %s in %s, and the guest needs y.\n", s, v, cfg > "/dev/stderr"
            print "Try: Set it, run make olddefconfig, and commit the config" > "/dev/stderr"
            bad = 1
        }
    }

    lsm = ("LSM" in val) ? val["LSM"] : ""
    gsub(/^"|"$/, "", lsm)
    has_landlock = 0
    nlsm = split(lsm, items, ",")
    for (i = 1; i <= nlsm; i++) {
        if (items[i] == "landlock") {
            has_landlock = 1
            break
        }
    }
    if (!has_landlock) {
        printf "check-config: CONFIG_LSM lacks landlock in %s, and the guest needs it.\n", cfg > "/dev/stderr"
        print "Try: Add landlock to CONFIG_LSM, run make olddefconfig, and commit the config" > "/dev/stderr"
        bad = 1
    }

    nforb = split(forb_str, forb, " ")
    for (i = 1; i <= nforb; i++) {
        s = forb[i]
        v = (s in val) ? val[s] : "n"
        if (v != "n") {
            printf "check-config: CONFIG_%s is %s in %s, and the guest forbids it.\n", s, v, cfg > "/dev/stderr"
            print "Try: Unset it, run make olddefconfig, and commit the config" > "/dev/stderr"
            bad = 1
        }
    }

    if (bad) {
        exit 1
    }
}
' "$config"
