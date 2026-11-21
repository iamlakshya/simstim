#!/bin/sh
# Run as root. Keep this file until every recorded step has been undone.
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
umask 022
fail() { printf 'Error: %s\nTry: ask the machine owner to check the root-step records.\n' "$*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || fail 'run this undo as root'
u='@USER@'
account_uid=$(id -u "$u" 2>/dev/null || true)
[ -z "$account_uid" ] || [ "$account_uid" = '@UID@' ] || fail 'the account uid changed'
base='/var/lib/@BRAND@/root-steps'
d="$base/$u"
safe() {
    [ ! -L "$1" ] || fail "symbolic link at $1"
    [ "$(stat -c %u "$1")" = 0 ] || fail "non-root owner at $1"
    mode=$(stat -c %a "$1")
    [ $((0$mode & 022)) = 0 ] || fail "writable root-step path $1"
}
for p in /var /var/lib /var/lib/@BRAND@ "$base" "$d" "$d/journal" "$base/.lock"; do safe "$p"; done
exec 9>"$base/.lock"
flock -n 9 || fail 'another root step or undo is running'
restore() {
    target=$1
    source=$2
    [ ! -L "$target" ] || fail "symbolic link at $target"
    if [ -e "$source.before" ]; then
        safe "$source.before"
        tmp=$(mktemp "${target}.XXXXXXXX")
        cp -p "$source.before" "$tmp" && mv -f "$tmp" "$target"
    elif [ -e "$source.absent" ]; then
        rm -f "$target"
    fi
}
machine_json() {
    tmp=$(mktemp "$base/machine.XXXXXXXX") || return 1
    printf '{"version":1,"steps":[' > "$tmp" || return 1
    sep=''
    for mid in io-uring-off vsock-modules; do
        m="$base/machine/$mid"
        [ -d "$m" ] || continue
        printf '%s{"id":"%s","file_present":%s,"before":' "$sep" "$mid" "$(if [ -e "$m/file.before" ]; then echo true; else echo false; fi)" >> "$tmp" || return 1
        if [ -f "$m/value" ]; then cat "$m/value" >> "$tmp" || return 1; else printf null >> "$tmp" || return 1; fi
        printf ',"users":[' >> "$tmp" || return 1
        usep=''
        for ref in "$m"/users/*; do
            [ -f "$ref" ] || continue
            printf '%s"%s"' "$usep" "${ref##*/}" >> "$tmp" || return 1
            usep=,
        done
        printf ']}' >> "$tmp" || return 1
        sep=,
    done
    printf ']}\n' >> "$tmp" || return 1
    chmod 0644 "$tmp" || return 1
    mv -f "$tmp" "$base/machine.json" || return 1
}
undo_machine() {
    mid=$1
    m="$base/machine/$mid"
    [ -d "$m" ] || return 0
    safe "$m"
    safe "$m/users"
    rm -f "$m/users/$u" || return 1
    remaining=$(find "$m/users" -mindepth 1 -maxdepth 1 -type f -printf x) || return 1
    if [ -z "$remaining" ]; then
        case "$mid" in
            io-uring-off)
                value=$(cat "$m/value")
                case "$value" in 0|1|2) ;; *) fail 'invalid recorded io_uring value';; esac
                restore '/etc/sysctl.d/50-@BRAND@.conf' "$m/file" || return 1
                sysctl -w "kernel.io_uring_disabled=$value" || return 1
                ;;
            vsock-modules) restore '/etc/modprobe.d/50-@BRAND@-vsock.conf' "$m/file" || return 1 ;;
        esac
        rm -rf "$m" || return 1
    fi
    machine_json
}
applied() {
    tmp=$(mktemp "$d/applied.XXXXXXXX")
    printf '{"version":1,"user":"%s","steps":[' "$u" > "$tmp"
    sep=''
    for entry in "$d"/journal/*; do
        [ -f "$entry/receipt.json" ] || continue
        printf '%s"%s"' "$sep" "$(cat "$entry/id")" >> "$tmp"
        sep=,
    done
    printf '],"records":[' >> "$tmp"
    sep=''
    for entry in "$d"/journal/*; do
        [ -f "$entry/receipt.json" ] || continue
        printf '%s' "$sep" >> "$tmp"
        cat "$entry/receipt.json" >> "$tmp"
        sep=,
    done
    printf ']}\n' >> "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$d/applied.json"
}
# The native anchor is private to this original journal. These checks are
# best effort under the trusted host-root/logind boundary, not an atomic
# conditional namespace operation against an independent privileged writer.
undo_linger_origin() (
    parent='/var/lib/systemd/linger'
    left_linger() { printf 'LEFT linger %s\n' "$1"; return 2; }
    protected_dir() {
        [ ! -L "$1" ] && [ -d "$1" ] && [ "$(stat -c %u "$1" 2>/dev/null)" = 0 ] || return 1
        mode=$(stat -c %a "$1" 2>/dev/null) || return 1
        printf '%s\n' "$mode" | grep -Eq '^[0-7]{3,4}$' && [ $((0$mode & 022)) = 0 ]
    }
    read_linger() {
        file="$j/linger.$1"
        [ ! -L "$file" ] && [ -f "$file" ] &&
        [ "$(stat -c '%u:%g:%a:%h' "$file" 2>/dev/null)" = '0:0:600:1' ] &&
        [ "$(stat -c %s "$file" 2>/dev/null)" -le 512 ] &&
        [ "$(wc -l < "$file")" -eq 1 ]
    }
    for path in /var /var/lib /var/lib/systemd "$d" "$d/journal" "$j"; do
        protected_dir "$path" || { left_linger unsafe_parent; return 2; }
    done
    [ "$(id -u "$u" 2>/dev/null)" = '@UID@' ] || { left_linger account_changed; return 2; }
    [ "$(cat "$j/id")" = linger ] || { left_linger unknown_journal; return 2; }
    read_linger intent || { left_linger missing_origin; return 2; }
    IFS=' ' read -r v generation recorded_uid boot ancestor journal prior extra < "$j/linger.intent" || { left_linger malformed_intent; return 2; }
    [ "$v" = 1 ] && [ "$recorded_uid" = '@UID@' ] && [ -z "$extra" ] || { left_linger unknown_intent; return 2; }
    printf '%s\n' "$generation" | grep -Eq '^[0-9a-f]{32}$' || { left_linger unknown_generation; return 2; }
    printf '%s\n' "$boot" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' || { left_linger unknown_boot; return 2; }
    case "$prior" in missing|existing) ;; *) left_linger unknown_parent; return 2;; esac
    read_linger parent || { left_linger missing_parent_origin; return 2; }
    IFS=' ' read -r pv pg identity created extra < "$j/linger.parent" || { left_linger malformed_parent; return 2; }
    [ "$pv" = 1 ] && [ "$pg" = "$generation" ] && [ -z "$extra" ] || { left_linger unknown_parent; return 2; }
    case "$prior:$created" in missing:created|existing:existing) ;; *) left_linger unknown_parent_origin; return 2;; esac
    read_linger origin-id || { left_linger missing_origin; return 2; }
    IFS=' ' read -r ov og original extra < "$j/linger.origin-id" || { left_linger malformed_origin; return 2; }
    [ "$ov" = 1 ] && [ "$og" = "$generation" ] && [ -z "$extra" ] || { left_linger unknown_origin; return 2; }
    for native in "$ancestor" "$journal" "$identity" "$original"; do
        printf '%s\n' "$native" | grep -Eq '^[0-9]+:[1-9][0-9]*$' || { left_linger unknown_native_identity; return 2; }
    done
    if [ -e "$j/linger.result" ] || [ -L "$j/linger.result" ]; then
        read_linger result || { left_linger unknown_result; return 2; }
        IFS=' ' read -r rv rg phase extra < "$j/linger.result" || { left_linger malformed_result; return 2; }
        [ "$rv" = 1 ] && [ "$rg" = "$generation" ] && [ -z "$extra" ] || { left_linger unknown_result; return 2; }
        case "$phase" in created|enabled|enable_failed|disable_pending|disabled|already_absent|parent_removed) ;; *) left_linger future_result; return 2;; esac
    fi
    exec 4</var/lib/systemd
    exec 6<"$j"
    base_parents() {
        protected_dir /var/lib/systemd && protected_dir "$j" &&
        [ "$(stat -Lc '%d:%i' /proc/self/fd/4)" = "$ancestor" ] &&
        [ "$(stat -c '%d:%i' /var/lib/systemd)" = "$ancestor" ] &&
        [ "$(stat -Lc '%d:%i' /proc/self/fd/6)" = "$journal" ] &&
        [ "$(stat -c '%d:%i' "$j")" = "$journal" ]
    }
    base_parents || { left_linger parent_changed; return 2; }
    parent_entries=$(find -H /proc/self/fd/4 -mindepth 1 -maxdepth 1 -name linger -printf x) || { left_linger unreadable_parent; return 2; }
    if [ -z "$parent_entries" ]; then
        # A prior successful disable/rmdir may have lost its final journal
        # acknowledgement. No system path is removed or recreated on retry.
        [ "$created" = created ] || { left_linger parent_changed; return 2; }
        case "${phase:-}" in disabled|already_absent|parent_removed) ;; *) left_linger parent_changed; return 2;; esac
        [ ! -L "$j/linger.origin" ] && [ -f "$j/linger.origin" ] || { left_linger missing_native_anchor; return 2; }
        exec 8<"$j/linger.origin"
        [ "$(stat -Lc '%d:%i' /proc/self/fd/8)" = "$original" ] &&
        [ "$(stat -Lc '%u:%g:%a:%s:%h' /proc/self/fd/8)" = '0:0:644:0:1' ] || { left_linger anchor_changed; return 2; }
        return 0
    fi
    [ "$parent_entries" = x ] && protected_dir "$parent" || { left_linger unsafe_linger_parent; return 2; }
    exec 7<"$parent"
    same_parents() {
        protected_dir /var/lib/systemd && protected_dir "$j" && protected_dir "$parent" &&
        [ "$(stat -Lc '%d:%i' /proc/self/fd/4)" = "$ancestor" ] &&
        [ "$(stat -c '%d:%i' /var/lib/systemd)" = "$ancestor" ] &&
        [ "$(stat -Lc '%d:%i' /proc/self/fd/6)" = "$journal" ] &&
        [ "$(stat -c '%d:%i' "$j")" = "$journal" ] &&
        [ "$(stat -Lc '%d:%i' /proc/self/fd/7)" = "$identity" ] &&
        [ "$(stat -c '%d:%i' "$parent")" = "$identity" ]
    }
    same_parents || { left_linger parent_changed; return 2; }
    [ ! -L "$j/linger.origin" ] && [ -f "$j/linger.origin" ] || { left_linger missing_native_anchor; return 2; }
    exec 8<"$j/linger.origin"
    anchor_shape() {
        [ ! -L "$j/linger.origin" ] && [ -f /proc/self/fd/8 ] &&
        [ "$(stat -Lc '%d:%i' /proc/self/fd/8)" = "$original" ] &&
        [ "$(stat -c '%d:%i' "$j/linger.origin")" = "$original" ] &&
        [ "$(stat -Lc '%u:%g:%a:%s:%h' /proc/self/fd/8)" = "0:0:644:0:$1" ]
    }
    target_entries() {
        # A successful native directory read proves absence; a failed stat
        # alone cannot turn an I/O or permission error into an absent file.
        find -H /proc/self/fd/7 -mindepth 1 -maxdepth 1 -name "$u" -printf x
    }
    write_result() {
        tmp=$(mktemp "$j/.linger-result.XXXXXXXX") || return 1
        printf '1 %s %s\n' "$generation" "$1" > "$tmp" && chown 0:0 "$tmp" && chmod 0600 "$tmp" && sync -f "$tmp" || return 1
        mv -T -- "$tmp" "$j/linger.result" && sync -f "$j"
    }
    entries=$(target_entries) || { left_linger unreadable_target; return 2; }
    case "$entries" in
        '')
            anchor_shape 1 || { left_linger anchor_changed; return 2; }
            write_result already_absent || { left_linger result_not_saved; return 2; }
            ;;
        x)
            [ ! -L "$parent/$u" ] && [ -f "$parent/$u" ] || { left_linger target_changed; return 2; }
            exec 5<"$parent/$u"
            same_target() {
                same_parents && anchor_shape 2 && [ ! -L "$parent/$u" ] &&
                [ "$(stat -Lc '%d:%i' /proc/self/fd/5)" = "$original" ] &&
                [ "$(stat -c '%d:%i' "$parent/$u")" = "$original" ]
            }
            same_target || { left_linger target_changed; return 2; }
            write_result disable_pending || { left_linger result_not_saved; return 2; }
            same_target || { left_linger target_changed; return 2; }
            # Existing Root operation serialises our actors. This fixed call
            # is NOT atomic against independent privileged root/logind work.
            if ! loginctl disable-linger "$u"; then left_linger disable_failed; return 2; fi
            same_parents && anchor_shape 1 || { left_linger postcheck_changed; return 2; }
            entries=$(target_entries) || { left_linger unreadable_postcheck; return 2; }
            [ -z "$entries" ] || { left_linger target_reappeared; return 2; }
            write_result disabled || { left_linger result_not_saved; return 2; }
            ;;
        *) left_linger ambiguous_target; return 2;;
    esac
    if [ "$created" = created ]; then
        same_parents || { left_linger parent_changed; return 2; }
        entries=$(find -H /proc/self/fd/7 -mindepth 1 -maxdepth 1 -printf x) || { left_linger unreadable_parent; return 2; }
        [ -z "$entries" ] || { left_linger parent_not_empty; return 2; }
        rmdir -- "$parent" || { left_linger parent_not_removed; return 2; }
        entries=$(find -H /proc/self/fd/4 -mindepth 1 -maxdepth 1 -name linger -printf x) || { left_linger unreadable_parent; return 2; }
        [ -z "$entries" ] || { left_linger parent_reappeared; return 2; }
        write_result parent_removed || { left_linger result_not_saved; return 2; }
    fi
)
undo_step() {
    if [ -f "$j/unchanged" ]; then
        if [ "$step" = linger ]; then printf '%s\n' 'LEFT linger pre_existing'; return 2; fi
        return 0
    fi
    case "$step" in
        nitro-acl)
            [ ! -L /dev/nitro_enclaves ] && [ -c /dev/nitro_enclaves ] || return 1
            if [ -s "$j/acl.before" ]; then setfacl -m "$(cat "$j/acl.before")" /dev/nitro_enclaves; else setfacl -x 'u:@USER@' /dev/nitro_enclaves; fi ;;
        nitro-udev)
            path='/etc/udev/rules.d/50-@BRAND@-nitro.rules'
            [ ! -L "$path" ] && [ -f "$path" ] || return 1
            safe "$path"
            line='ACTION=="add|change", SUBSYSTEM=="misc", KERNEL=="nitro_enclaves", RUN+="/usr/bin/setfacl -m u:@USER@:rw /dev/nitro_enclaves"'
            tmp=$(mktemp '/etc/udev/rules.d/.@BRAND@-nitro.XXXXXXXX') || return 1
            awk -v line="$line" '$0 != line' "$path" > "$tmp" && chmod 0644 "$tmp" && mv -f "$tmp" "$path" || return 1 ;;
        nitro-slots)
            [ -f "$j/slots.boot" ] && [ -f "$j/slots.before" ] || return 1
            if [ "$(cat "$j/slots.boot")" = "$(cat /proc/sys/kernel/random/boot_id)" ]; then
                printf '%s\n' 'LEFT nitro-slots donation retained until reboot'
                return 2
            fi ;;
        kvm-acl) setfacl -x 'u:@USER@' /dev/kvm ;;
        kvm-group) gpasswd -d '@USER@' kvm ;;
        linger) undo_linger_origin ;;
        delegate-io)
            restore '/etc/systemd/system/user@@UID@.service.d/50-@BRAND@-delegate.conf' "$j/file" && systemctl daemon-reload ;;
        subuid)
            [ -f "$j/range" ] || return 0
            range=$(cat "$j/range")
            printf '%s\n' "$range" | awk -F- 'NF != 2 || $1 !~ /^[0-9]+$/ || $2 !~ /^[0-9]+$/ || $2-$1 != 65535 { exit 1 }' || fail 'invalid subordinate ID record'
            for database in subuid subgid; do
                if [ -s "/etc/$database" ]; then
                    last=$(tail -c 1 "/etc/$database" | od -An -tu1)
                    [ "$last" -eq 10 ] || fail "truncated /etc/$database needs owner repair before undo"
                fi
            done
            for database in subuid subgid; do
                awk -F: '$0 != "" && $0 !~ /^#/ && (NF != 3 || $1 == "" || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ || $3 < 1 || $2+$3-1 > 4294967294) { exit 1 }' "/etc/$database" || fail "malformed /etc/$database needs owner repair before undo"
            done
            # Native shadow holds its own locks and subtracts only this owner's
            # range. It preserves all other users and IDs outside the range.
            usermod --del-subuids "$range" --del-subgids "$range" '@USER@' ;;
        io-uring-off|vsock-modules) undo_machine "$step" ;;
        *) fail 'unknown step in the root journal' ;;
    esac
}
status=0
left=0
# Numeric journal names and the checked user have no shell separators or spaces.
for j in $(printf '%s\n' "$d"/journal/* | sort -r); do
    [ -d "$j" ] || continue
    safe "$j"
    safe "$j/id"
    step=$(cat "$j/id")
    printf 'Undo %s\n' "$step"
    if undo_step; then
        rm -rf "$j"
        applied
    else
        result=$?
        if [ "$result" = 2 ] && { [ "$step" = linger ] || [ "$step" = nitro-slots ]; }; then
            left=1
        else
            status=1
            printf 'Could not undo %s. Keep this script and retry.\n' "$step" >&2
        fi
    fi
 done
if [ "$status" != 0 ]; then exit "$status"; fi
if [ "$left" = 0 ]; then
    rm -f "$d/applied.json" "$d/undo.sh" "$d/subid-allocate.sh"
    rmdir "$d/journal" "$d"
    printf '%s\n' __ROOT_STEP_UNDONE__
else
    # Left is a completed attempt with retained evidence, never all-undone.
    printf '%s\n' __ROOT_STEP_LEFT__
fi
