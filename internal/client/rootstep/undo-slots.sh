# Included only in the slots case of undo_step. Remove the one exact rule
# before touching slot identities; launcher undo restores any prior file.
sudoers='/etc/sudoers.d/@BRAND@-@USER@'
if [ -e "$sudoers" ]; then
    safe "$sudoers"
    expected=$(printf '%s\n' '@USER@ ALL=(root) NOPASSWD: /usr/local/libexec/@BRAND@/launch ""')
    [ "$(cat "$sudoers")" = "$expected" ] || fail 'the sudoers rule changed; keep the slot accounts and ask the owner'
    rm -f "$sudoers" || return 1
fi
m="$base/machine/slots"
if [ -d "$m" ]; then
    safe "$m"
    safe "$m/users"
    if [ -d "$m/capacity" ]; then
        safe "$m/capacity"
        store_lock='/var/lib/@BRAND@/store/.lock'
        safe "$store_lock"
        exec 8>"$store_lock"
        flock 8
        projection="$m/capacity/@UID@.json"
        if [ -e "$projection" ] || [ -L "$projection" ]; then
            safe "$projection"
            [ -f "$projection" ] && [ "$(stat -c %h "$projection")" = 1 ] || fail 'the slot projection is not a single regular file'
            rm -f "$projection" || return 1
        fi
        flock -u 8
        exec 8>&-
    fi
    rm -f "$m/users/$u" || return 1
    remaining=$(find "$m/users" -mindepth 1 -maxdepth 1 -type f -printf x) || return 1
    if [ -z "$remaining" ]; then
        n=0
        while [ "$n" -lt 16 ]; do
            slot=$(printf '%02d' "$n")
            lock="/var/lib/@BRAND@/slots/$slot.lock"
            if [ ! -e "$lock" ]; then n=$((n + 1)); continue; fi
            safe "$lock"
            # Every original OFD must be free. A stale status file or
            # numeric process check never substitutes for the kernel lock.
            flock -n "$lock" true || fail 'a sandbox still holds a slot; stop it before undo'
            n=$((n + 1))
        done
        if [ -f "$m/nitro-rules.before" ]; then
            rules='/etc/udev/rules.d/50-@BRAND@-nitro.rules'
            safe "$rules"
            safe "$m/nitro-rules.before"
            tmp=$(mktemp '/etc/udev/rules.d/.@BRAND@-nitro.XXXXXXXX') || return 1
            # Remove only slot entries this step added. Later login-user
            # rules and all original entries retain their current bytes.
            awk '
                BEGIN { for (i = 0; i < 16; i++) { slot = sprintf("%02d", i); added["ACTION==\"add|change\", SUBSYSTEM==\"misc\", KERNEL==\"nitro_enclaves\", RUN+=\"/usr/bin/setfacl -m u:@BRAND@-s" slot ":rw /dev/nitro_enclaves\""] = 1 } }
                FILENAME == ARGV[1] { original[$0] = 1; next }
                !($0 in added) || ($0 in original) { print }
            ' "$m/nitro-rules.before" "$rules" > "$tmp" && chmod 0644 "$tmp" && mv -f "$tmp" "$rules" || return 1
        fi
        if [ -e /dev/nitro_enclaves ]; then
            n=0
            while [ "$n" -lt 16 ]; do
                slot=$(printf '%02d' "$n")
                if [ -f "$m/nitro-$slot.before" ]; then
                    before=$(cat "$m/nitro-$slot.before")
                    if [ -n "$before" ]; then setfacl -m "$before" /dev/nitro_enclaves || return 1; else setfacl -x "u:@BRAND@-s$slot" /dev/nitro_enclaves || return 1; fi
                fi
                n=$((n + 1))
            done
        fi
        if [ -f "$m/created-accounts" ]; then
            while IFS=: read -r account saved_uid; do
                case "$account" in @BRAND@-s[01][0-9]) ;; *) fail 'invalid recorded slot account';; esac
                [ "$(id -u "$account" 2>/dev/null || true)" = "$saved_uid" ] || fail 'a slot account uid changed'
                [ -z "$(pgrep -u "$saved_uid" || true)" ] || fail 'a process of a slot account still runs'
                userdel "$account" || return 1
            done < "$m/created-accounts"
        fi
        if [ -f "$m/added-kvm" ]; then
            while IFS= read -r account; do
                if getent passwd "$account" >/dev/null; then gpasswd -d "$account" kvm || return 1; fi
            done < "$m/added-kvm"
        fi
        # Session disks and immutable store files are never recursively
        # deleted by undo. The owner may remove them after export/purge.
        rm -rf "$m" || return 1
    fi
fi
