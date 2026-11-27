if begin nitro-slots; then
    command -v setfacl >/dev/null || fail 'setfacl is missing, install package acl by hand'
    [ "$(stat -f -c %T /sys)" = sysfs ] || fail 'Nitro slot counters need the kernel sysfs'
    path='/sys/class/misc/nitro_enclaves/pcie_slots'
    previous=$(cat "$path")
    total=$(cat /sys/class/misc/nitro_enclaves/pcie_slots_total)
    case "$previous:$total" in *[!0-9:]*|:*|*:) fail 'unknown Nitro slot counters';; esac
    [ '@NITRO_SLOTS@' -le "$total" ] || fail 'the requested Nitro slots exceed this instance budget'
    printf '%s\n' "$previous" > "$j/slots.before"
    cat /proc/sys/kernel/random/boot_id > "$j/slots.boot"
    if [ '@NITRO_SLOTS@' -le "$previous" ]; then
        : > "$j/unchanged"
    else
        printf '%s\n' '@NITRO_SLOTS@' > "$path"
    fi
    actual=$(cat "$path")
    case "$actual" in ''|*[!0-9]*) fail 'unknown applied Nitro slot counter';; esac
    [ "$actual" -ge '@NITRO_SLOTS@' ] || fail 'the Nitro slot budget did not grow'
    printf '%s\n' "$actual" > "$j/slots.applied"
    extra=",\"before\":$previous,\"after\":$actual"
    record
    extra=''
fi
