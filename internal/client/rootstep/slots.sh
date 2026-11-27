if begin slots; then
    m="$base/machine/slots"
    dir "$m"
    dir "$m/users"
    nitro_rules='/etc/udev/rules.d/50-@BRAND@-nitro.rules'
    if [ -e "$nitro_rules" ] && [ ! -e "$m/nitro-rules.before" ]; then
        old_j=$j
        j=$m
        backup "$nitro_rules" nitro-rules
        j=$old_j
    fi
    for p in '/var/lib/@BRAND@/store' '/var/lib/@BRAND@/store/sha256' '/var/lib/@BRAND@/store/incoming' '/var/lib/@BRAND@/slots' '/var/lib/@BRAND@/sessions' '/var/lib/@BRAND@/vms'; do
        if [ ! -e "$p" ]; then mkdir -m 0755 "$p"; printf '%s\n' "$p" >> "$m/created-dirs"; fi
        [ -d "$p" ] || fail 'a slot store path is not a directory'
        safe "$p"
    done
    chmod 0700 '/var/lib/@BRAND@/store/incoming'
    n=0
    while [ "$n" -lt 16 ]; do
        slot=$(printf '%02d' "$n")
        account="@BRAND@-s$slot"
        if ! getent passwd "$account" >/dev/null; then
            useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$account"
            # Keep the exact uid: undo never deletes a later account that
            # reused a name or an account supplied by the machine's owner.
            printf '%s:%s\n' "$account" "$(id -u "$account")" >> "$m/created-accounts"
        fi
        entry=$(getent passwd "$account")
        printf '%s\n' "$entry" | awk -F: -v n="$account" 'NF != 7 || $1 != n || $3 !~ /^[0-9]+$/ || $3 == 0 || $4 !~ /^[0-9]+$/ || $4 == 0 || $6 != "/nonexistent" || $7 != "/usr/sbin/nologin" { exit 1 }' || fail 'a pre-existing slot account has unsafe fields'
        # Never grant a slot extra groups. Pre-existing unexpected groups
        # require the owner's repair, rather than removing their access.
        for group in $(id -nG "$account"); do case "$group" in "$account"|kvm) ;; *) fail 'a slot account has an unexpected group';; esac; done
        if getent group kvm >/dev/null && ! id -nG "$account" | tr ' ' '\n' | grep -qx kvm; then
            usermod -aG kvm "$account"
            printf '%s\n' "$account" >> "$m/added-kvm"
        fi
        if [ -e /dev/nitro_enclaves ]; then
            [ ! -L /dev/nitro_enclaves ] && [ -c /dev/nitro_enclaves ] || fail 'the Nitro device changed its native type'
            command -v getfacl >/dev/null || fail 'the Nitro device needs getfacl installed by its owner'
            command -v setfacl >/dev/null || fail 'the Nitro device needs acl installed by its owner'
            if [ ! -f "$m/nitro-$slot.before" ]; then getfacl -cp /dev/nitro_enclaves | awk -F: -v n="$account" '$1 == "user" && $2 == n { print $1 ":" $2 ":" $3 }' > "$m/nitro-$slot.before"; fi
            setfacl -m "u:$account:rw" /dev/nitro_enclaves
        fi
        lock="/var/lib/@BRAND@/slots/$slot.lock"
        [ ! -L "$lock" ] || fail 'a slot lock is a symbolic link'
        if [ ! -e "$lock" ]; then (set -C; : > "$lock"); chmod 0644 "$lock"; fi
        safe "$lock"
        n=$((n + 1))
    done
    if [ -f "$m/nitro-rules.before" ] && [ -e /dev/nitro_enclaves ]; then
        tmp=$(mktemp /etc/udev/rules.d/.@BRAND@-nitro.XXXXXXXX)
        safe "$nitro_rules"
        cat "$nitro_rules" > "$tmp"
        printf '\n' >> "$tmp"
        n=0
        while [ "$n" -lt 16 ]; do
            slot=$(printf '%02d' "$n")
            line=$(printf 'ACTION=="add|change", SUBSYSTEM=="misc", KERNEL=="nitro_enclaves", RUN+="/usr/bin/setfacl -m u:@BRAND@-s%s:rw /dev/nitro_enclaves"' "$slot")
            if ! grep -Fqx "$line" "$tmp"; then printf '%s\n' "$line" >> "$tmp"; fi
            n=$((n + 1))
        done
        chmod 0644 "$tmp"
        mv -f "$tmp" "$nitro_rules"
    fi
    printf '%s\n' "$uid" > "$m/users/$u"
    chmod 0600 "$m/users/$u"
    # Derived advisory metadata carries no allocation or execution permission.
    # Keep an initial observation unknown until the native producer inspects it.
    dir "$m/capacity"
    store_lock='/var/lib/@BRAND@/store/.lock'
    [ ! -L "$store_lock" ] || fail 'the store lock is a symbolic link'
    if [ ! -e "$store_lock" ]; then (set -C; : > "$store_lock"); chmod 0644 "$store_lock"; fi
    safe "$store_lock"
    exec 8>"$store_lock"
    flock 8
    projection="$m/capacity/$uid.json"
    if [ -e "$projection" ] || [ -L "$projection" ]; then
        safe "$projection"
        [ -f "$projection" ] && [ "$(stat -c %h "$projection")" = 1 ] || fail 'the slot projection is not a single regular file'
    fi
    boot=$(cat /proc/sys/kernel/random/boot_id)
    generation=$(cat /proc/sys/kernel/random/uuid | tr -d '-')
    printf '%s\n' "$boot" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' || fail 'the current boot identity is unavailable'
    printf '%s\n' "$generation" | grep -Eq '^[0-9a-f]{32}$' || fail 'the projection generation is unavailable'
    tmp=$(mktemp "$m/capacity/.projection.XXXXXXXX")
    printf '{"uid":%s,"boot_id":"%s","generation":"%s","observed_unix":%s}\n' "$uid" "$boot" "$generation" "$(date -u +%s)" > "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$projection"
    flock -u 8
    exec 8>&-
    record
fi
