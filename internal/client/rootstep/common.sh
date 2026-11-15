#!/bin/sh
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
umask 022
fail() { printf 'Error: %s\nTry: ask the machine owner to check the root-step records.\n' "$*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || fail 'run this script as root'
u='@USER@'
uid='@UID@'
[ "$(id -u "$u")" = "$uid" ] || fail 'the account uid changed'
base='/var/lib/@BRAND@/root-steps'
d="$base/$u"
safe() {
    [ ! -L "$1" ] || fail "symbolic link at $1"
    [ "$(stat -c %u "$1")" = 0 ] || fail "non-root owner at $1"
    mode=$(stat -c %a "$1")
    [ $((0$mode & 022)) = 0 ] || fail "writable root-step path $1"
}
dir() { if [ ! -e "$1" ]; then mkdir -m 0755 "$1"; fi; [ -d "$1" ] || fail "not a directory: $1"; safe "$1"; }
for p in /var /var/lib /var/lib/@BRAND@ "$base"; do dir "$p"; done
[ ! -L "$base/.lock" ] || fail 'unsafe lock'
if [ ! -e "$base/.lock" ]; then (set -C; : > "$base/.lock"); fi
safe "$base/.lock"
exec 9>"$base/.lock"
flock -n 9 || fail 'another root step or undo is running'
dir "$d"
dir "$d/journal"
dir "$base/machine"
# All journal state is root-owned. An incomplete step must be undone first.
begin() {
    step=$1
    for old in "$d"/journal/*; do
        [ -d "$old" ] || continue
        safe "$old"
        [ -f "$old/id" ] || fail 'incomplete journal'
        if [ "$(cat "$old/id")" = "$step" ]; then
            [ -f "$old/receipt.json" ] || fail 'undo the incomplete root step before retrying'
            return 1
        fi
    done
    n=$(find "$d/journal" -mindepth 1 -maxdepth 1 -type d | wc -l)
    n=$((n + 1))
    j=$(printf '%s/journal/%08d' "$d" "$n")
    mkdir -m 0700 "$j" || fail 'cannot create the root-step journal'
    printf '%s\n' "$step" > "$j/id" || fail 'cannot write the root-step journal'
    # undo.sh already contains the undo for every supported step.
    chmod 0600 "$j/id" || fail 'cannot protect the root-step journal'
}
backup() {
    path=$1
    key=$2
    [ ! -L "$path" ] || fail "symbolic link at $path"
    if [ -e "$path" ]; then
        [ -f "$path" ] || fail "not a regular file: $path"
        safe "$path"
        cp -p "$path" "$j/$key.before"
    else
        : > "$j/$key.absent"
    fi
}
# JSON receipts contain only fixed IDs, checked account names and numbers.
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
record() {
    tmp=$(mktemp "$j/receipt.XXXXXXXX")
    printf '{"id":"%s","at":"%s"%s}\n' "$step" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${extra:-}" > "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$j/receipt.json"
    applied
}
machine_json() {
    tmp=$(mktemp "$base/machine.XXXXXXXX")
    printf '{"version":1,"steps":[' > "$tmp"
    sep=''
    for mjid in io-uring-off vsock-modules; do
        mjm="$base/machine/$mjid"
        [ -d "$mjm" ] || continue
        printf '%s{"id":"%s","file_present":%s,"before":' "$sep" "$mjid" "$(if [ -e "$mjm/file.before" ]; then echo true; else echo false; fi)" >> "$tmp"
        if [ -f "$mjm/value" ]; then cat "$mjm/value" >> "$tmp"; else printf null >> "$tmp"; fi
        printf ',"users":[' >> "$tmp"
        usep=''
        for ref in "$mjm"/users/*; do
            [ -f "$ref" ] || continue
            printf '%s"%s"' "$usep" "${ref##*/}" >> "$tmp"
            usep=,
        done
        printf ']}' >> "$tmp"
        sep=,
    done
    printf ']}\n' >> "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$base/machine.json"
}
machine_begin() {
    mid=$1
    path=$2
    m="$base/machine/$mid"
    if [ ! -d "$m" ]; then
        mkdir -m 0700 "$m"
        mkdir -m 0700 "$m/users"
        backup "$path" file
        if [ -e "$j/file.before" ]; then cp -p "$j/file.before" "$m/file.before"; else : > "$m/file.absent"; fi
        if [ "$mid" = io-uring-off ]; then
            value=$(sysctl -n kernel.io_uring_disabled)
            case "$value" in 0|1|2) ;; *) fail 'unknown io_uring setting';; esac
            printf '%s\n' "$value" > "$m/value"
        fi
    fi
    safe "$m"
    safe "$m/users"
    : > "$m/users/$u"
    machine_json
    m="$base/machine/$mid"
}

