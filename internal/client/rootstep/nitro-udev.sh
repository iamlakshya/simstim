if begin nitro-udev; then
    command -v setfacl >/dev/null || fail 'setfacl is missing, install package acl by hand'
    [ -x /usr/bin/setfacl ] || fail 'udev needs the system setfacl at /usr/bin/setfacl'
    for p in /etc /etc/udev /etc/udev/rules.d; do dir "$p"; done
    path='/etc/udev/rules.d/50-@BRAND@-nitro.rules'
    backup "$path" file
    line='ACTION=="add|change", SUBSYSTEM=="misc", KERNEL=="nitro_enclaves", RUN+="/usr/bin/setfacl -m u:@USER@:rw /dev/nitro_enclaves"'
    if [ -f "$path" ] && grep -Fqx "$line" "$path"; then
        : > "$j/unchanged"
    else
        tmp=$(mktemp '/etc/udev/rules.d/.@BRAND@-nitro.XXXXXXXX')
        if [ -f "$path" ]; then cat "$path" > "$tmp"; printf '\n' >> "$tmp"; fi
        printf '%s\n' "$line" >> "$tmp"
        chmod 0644 "$tmp"
        mv -f "$tmp" "$path"
    fi
    record
fi
