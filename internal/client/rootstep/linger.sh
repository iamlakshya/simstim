# This private journal anchors the actual created inode. Receipt/status data
# never adopts a pre-existing linger file or grants a later root execution.
linger_created_origin() (
    parent='/var/lib/systemd/linger'
    for path in /var /var/lib /var/lib/systemd "$d" "$d/journal" "$j"; do
        [ -d "$path" ] || fail 'a fixed linger ancestor is unavailable'
        safe "$path"
    done
    [ "$(id -u "$u")" = "$uid" ] || fail 'the linger account changed'
    exec 4</var/lib/systemd
    exec 6<"$j"
    ancestor=$(stat -Lc '%d:%i' /proc/self/fd/4)
    journal=$(stat -Lc '%d:%i' /proc/self/fd/6)
    same_parents() {
        [ ! -L /var/lib/systemd ] && [ ! -L "$j" ] &&
        [ "$(stat -c '%d:%i' /var/lib/systemd)" = "$ancestor" ] &&
        [ "$(stat -c '%d:%i' "$j")" = "$journal" ]
    }
    write_linger() {
        case "$1" in intent|parent|origin-id|result) ;; *) fail 'unknown linger journal role';; esac
        tmp=$(mktemp "$j/.linger.XXXXXXXX") || return 1
        printf '%s\n' "$2" > "$tmp" && chown 0:0 "$tmp" && chmod 0600 "$tmp" && sync -f "$tmp" || return 1
        mv -T -- "$tmp" "$j/linger.$1" && sync -f "$j"
    }
    if [ -e "$parent" ] || [ -L "$parent" ]; then
        [ -d "$parent" ] || fail 'the fixed linger parent is not a directory'
        safe "$parent"
        prior=existing
    else
        prior=missing
    fi
    if [ "$prior" = existing ] && { [ -e "$parent/$u" ] || [ -L "$parent/$u" ]; }; then
        # Never retrofit creation ownership from an already-on status.
        : > "$j/unchanged"
        chmod 0600 "$j/unchanged"
        printf '%s\n' 'LEFT linger pre_existing'
        return 0
    fi
    if [ "$(loginctl show-user "$u" -p Linger --value 2>/dev/null || true)" = yes ]; then
        : > "$j/unchanged"
        chmod 0600 "$j/unchanged"
        printf '%s\n' 'LEFT linger pre_existing_status'
        return 0
    fi
    generation=$(cat /proc/sys/kernel/random/uuid | tr -d '-')
    boot=$(cat /proc/sys/kernel/random/boot_id)
    printf '%s\n' "$generation" | grep -Eq '^[0-9a-f]{32}$' || fail 'the native linger generation is unavailable'
    printf '%s\n' "$boot" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' || fail 'the native boot identity is unavailable'
    write_linger intent "1 $generation $uid $boot $ancestor $journal $prior" || fail 'cannot persist the ahead linger intent'
    same_parents || fail 'a fixed linger ancestor changed before creation'
    created=existing
    if [ "$prior" = missing ]; then
        # No -p or adoption after EEXIST: another creator owns that leaf.
        mkdir -m 0755 -- "$parent" || fail 'the missing linger parent changed before its exclusive creation'
        chown 0:0 "$parent" || fail 'cannot protect the created linger parent'
        created=created
    fi
    [ -d "$parent" ] || fail 'the native linger parent is unavailable'
    safe "$parent"
    exec 7<"$parent"
    identity=$(stat -Lc '%d:%i' /proc/self/fd/7)
    write_linger parent "1 $generation $identity $created" || fail 'cannot persist the native linger parent'
    (set -C; : > "$j/linger.origin") || fail 'the linger origin anchor already exists'
    chown 0:0 "$j/linger.origin" && chmod 0644 "$j/linger.origin" || fail 'cannot protect the created linger origin'
    exec 8<"$j/linger.origin"
    original=$(stat -Lc '%d:%i' /proc/self/fd/8)
    [ "$(stat -Lc '%u:%g:%a:%s:%h' /proc/self/fd/8)" = '0:0:644:0:1' ] || fail 'the created linger origin is not a private regular inode'
    [ -f /proc/self/fd/8 ] || fail 'the created linger origin is not regular'
    write_linger origin-id "1 $generation $original" || fail 'cannot persist the original linger inode'
    same_origin() {
        same_parents && [ ! -L "$parent" ] && [ ! -L "$parent/$u" ] && [ ! -L "$j/linger.origin" ] &&
        [ "$(stat -c '%d:%i' "$parent")" = "$identity" ] &&
        [ -f "$parent/$u" ] && [ "$(stat -c '%d:%i' "$parent/$u")" = "$original" ] &&
        [ "$(stat -c '%d:%i' "$j/linger.origin")" = "$original" ] &&
        [ "$(stat -Lc '%u:%g:%a:%s:%h' /proc/self/fd/8)" = '0:0:644:0:2' ]
    }
    # Exclusive publication creates only this exact inode at the fixed name.
    # Cross-filesystem links fail. There is no copy, force or replace fallback.
    same_parents && [ "$(stat -c '%d:%i' "$parent")" = "$identity" ] || fail 'the linger parent changed before publication'
    ln -T -- "$j/linger.origin" "$parent/$u" || fail 'the linger target was filled before exclusive publication'
    same_origin || fail 'the original linger publication changed'
    sync -f "$parent" || fail 'cannot persist the native linger publication'
    write_linger result "1 $generation created" || fail 'cannot persist the actual linger creation result'
    if loginctl enable-linger "$u"; then
        same_origin || fail 'the linger notification changed the original inode'
        write_linger result "1 $generation enabled" || fail 'cannot persist the linger notification result'
    else
        write_linger result "1 $generation enable_failed" || fail 'cannot persist the failed linger notification'
        fail 'the created linger inode remains recorded after a failed enable notification'
    fi
)
if begin linger; then
    linger_created_origin
    record
fi
