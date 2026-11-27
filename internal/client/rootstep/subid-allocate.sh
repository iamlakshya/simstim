#!/bin/sh
# This separate shell owns the PID locks and every database write itself.
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
umask 022
u='@USER@'
uid='@UID@'
d='/var/lib/@BRAND@/root-steps/@USER@'
j=${1:-}
fail() { printf 'Error: %s\nTry: keep the root-step records and ask the machine owner to check them.\n' "$*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || fail 'the allocator must run as root'
case "$j" in "$d"/journal/????????) ;; *) fail 'invalid journal path';; esac
case "${j##*/}" in *[!0-9]*) fail 'invalid journal number';; esac
safe() {
    [ ! -L "$1" ] || fail "symbolic link at $1"
    [ "$(stat -c %u "$1")" = 0 ] || fail "non-root owner at $1"
    mode=$(stat -c %a "$1")
    [ $((0$mode & 022)) = 0 ] || fail "writable account path $1"
}
for p in /etc /var /var/lib /var/lib/@BRAND@ /var/lib/@BRAND@/root-steps "$d" "$d/journal" "$j"; do safe "$p"; done
case "$(command -V printf)" in *'shell builtin'*) ;; *) fail 'this shell has no builtin printf';; esac
command -v newuidmap >/dev/null && command -v newgidmap >/dev/null || fail 'install package uidmap by hand first'
lock_nonce=$(mktemp '/etc/.@BRAND@-subid-lock.XXXXXXXX')
printf '%s\000' "$$" > "$lock_nonce"
chmod 0600 "$lock_nonce"
# Cleanup execs rm. Its PID stays live until its final unlink. There is no
# child that could later remove another account writer's replacement lock.
cleanup() {
    trap - 0 HUP INT TERM
    set --
    for db in subgid subuid group passwd; do
        if [ -e "/etc/$db.lock" ]; then
            if [ "$lock_nonce" -ef "/etc/$db.lock" ]; then
                set -- "$@" "/etc/$db.lock"
            fi
        fi
    done
    exec /bin/rm -f -- "$@" "$lock_nonce"
}
trap cleanup 0
trap 'exit 130' HUP INT TERM
# Fail fast because groupmod and useradd take conflicting lock orders.
for db in passwd group subuid subgid; do
    [ -f "/etc/$db" ] || fail "missing account database /etc/$db"
    safe "/etc/$db"
    ln "$lock_nonce" "/etc/$db.lock" 2>/dev/null || fail "account database $db is busy"
    [ "$lock_nonce" -ef "/etc/$db.lock" ] || fail 'an account lock changed'
done
if [ -f /etc/nsswitch.conf ]; then
    awk '/^[ \t]*subid[ \t]*:/ { sub(/#.*/, ""); sub(/^[^:]*:[ \t]*/, ""); sub(/[ \t]*$/, ""); if ($0 != "files") exit 1 }' /etc/nsswitch.conf || fail 'the subordinate ID provider is not local files'
fi
awk -F: -v user="$u" -v uid="$uid" '$1 == user { if ($3 != uid) exit 1; found++ } END { if (found != 1) exit 1 }' /etc/passwd || fail 'the local account changed'
for db in subuid subgid; do
    if [ -s "/etc/$db" ]; then
        last=$(tail -c 1 "/etc/$db" | od -An -tu1)
        [ "$last" -eq 10 ] || fail "unterminated record in /etc/$db"
    fi
done
defs=/etc/login.defs
if [ ! -f "$defs" ]; then defs=/dev/null; fi
range=$(awk -F: '
        FILENAME == "/etc/login.defs" {
            line=$0; sub(/^[ \t]+/, "", line); split(line, words, /[ \t]+/)
            if (words[1] == "SUB_UID_MIN") lo=words[2]
            if (words[1] == "SUB_UID_MAX") hi=words[2]
            next
        }
        FILENAME == "/etc/subuid" || FILENAME == "/etc/subgid" {
            if ($0 == "" || $0 ~ /^#/) next
            if (NF != 3 || $1 == "" || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ || $3 < 1 || $2+$3-1 > 4294967294) bad=1
            start[++n]=$2; end[n]=$2+$3-1; next
        }
        FILENAME == "/etc/passwd" || FILENAME == "/etc/group" {
            if ($0 == "" || $0 ~ /^#/) next
            if ($3 !~ /^[0-9]+$/ || $3 > 4294967294) bad=1
            start[++n]=$3; end[n]=$3
            if (FILENAME == "/etc/passwd") {
                if ($4 !~ /^[0-9]+$/ || $4 > 4294967294) bad=1
                start[++n]=$4; end[n]=$4
            }
        }
        END {
            if (lo == "") lo=100000
            if (hi == "") hi=600100000
            if (bad || lo !~ /^[0-9]+$/ || hi !~ /^[0-9]+$/ || lo < 1 || hi > 4294967294 || hi-lo < 65535) exit 1
            candidate=lo
            while (candidate+65535 <= hi) {
                nextid=candidate
                for (i=1;i<=n;i++)
                    if (candidate <= end[i] && candidate+65535 >= start[i] && end[i]+1 > nextid) nextid=end[i]+1
                if (nextid == candidate) { printf "%.0f-%.0f\n", candidate, candidate+65535; exit }
                candidate=nextid
            }
            exit 1
        }
    ' "$defs" /etc/subuid /etc/subgid /etc/passwd /etc/group) || fail 'no safe subordinate ID block is available'
printf '%s\n' "$range" > "$j/range"
start=${range%-*}
line="$u:$start:65536"
printf '%s\n' "$line" > "$j/subid.line"
# These are shell builtins in the lock holder. A killed holder cannot leave
# a surviving child that later writes a database after its PID becomes stale.
printf '%s\n' "$line" >> /etc/subuid
printf '%s\n' written > "$j/subuid.written"
printf '%s\n' "$line" >> /etc/subgid
printf '%s\n' written > "$j/subgid.written"
sync -f /etc/subuid /etc/subgid
printf '%s\n' complete > "$j/allocator.complete"
# cleanup execs with this same PID. The parent also checks allocator.complete.
exit 0
