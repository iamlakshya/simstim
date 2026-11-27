set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
umask 077
fail() { printf 'Error: %s\nTry: run host preparation again from the original client.\n' "$*" >&2; exit 1; }
[ "$(id -u)" = '@UID@' ] || fail 'the host account uid changed'
target='/var/tmp/@BRAND@-launch-@UID@-@LAUNCH_SHA256@'
checked() {
    [ ! -L "$target" ] && [ -f "$target" ] || fail 'the staged launcher is not a regular file'
    [ "$(stat -c %u "$target")" = '@UID@' ] || fail 'the staged launcher belongs to another account'
    [ "$(stat -c %h "$target")" = 1 ] || fail 'the staged launcher has another hard link'
    [ "$(stat -c %a "$target")" = 600 ] || fail 'the staged launcher has unsafe permissions'
    [ "$(stat -c %s "$target")" = '@LAUNCH_SIZE@' ] || fail 'the staged launcher has the wrong size'
    actual=$(dd if="$target" bs=1048576 count='@LAUNCH_SIZE@' iflag=count_bytes,nofollow,nonblock status=none | sha256sum | cut -d ' ' -f 1)
    [ "$actual" = '@LAUNCH_SHA256@' ] || fail 'the staged launcher differs from this release'
}
if [ '@VERIFY@' = 0 ]; then
    tmp=$(mktemp '/var/tmp/@BRAND@-launch-@UID@-@LAUNCH_SHA256@.XXXXXXXX')
    trap 'rm -f "$tmp"' EXIT HUP INT TERM
    dd of="$tmp" bs=1048576 count="$((@LAUNCH_SIZE@ + 1))" iflag=count_bytes status=none
    [ "$(stat -c %s "$tmp")" = '@LAUNCH_SIZE@' ] || fail 'the public launcher stream has the wrong size'
    [ "$(sha256sum "$tmp" | cut -d ' ' -f 1)" = '@LAUNCH_SHA256@' ] || fail 'the public launcher stream has the wrong digest'
    if [ -e "$target" ] || [ -L "$target" ]; then checked; else ln "$tmp" "$target" || fail 'another staging operation created the target'; fi
    rm -f "$tmp"
    trap - EXIT HUP INT TERM
else
    checked
    [ "$(stat -c %d "$target")" = '@DEVICE@' ] && [ "$(stat -c %i "$target")" = '@INODE@' ] || fail 'the original staged launcher inode changed'
fi
printf '{"version":1,"uid":@UID@,"size":@LAUNCH_SIZE@,"device":"%s","inode":"%s"}\n' "$(stat -c %d "$target")" "$(stat -c %i "$target")"
