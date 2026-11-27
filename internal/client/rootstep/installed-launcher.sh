set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
LC_ALL=C
export PATH LC_ALL
fail() { printf 'Error: %s\nTry: run the host doctor and keep the recorded undo script.\n' "$*" >&2; exit 1; }
[ "$(id -u)" = '@UID@' ] || fail 'the host account uid changed'
target='/usr/local/libexec/@BRAND@/launch'
[ ! -L "$target" ] && [ -f "$target" ] || fail 'the installed launcher is not a regular file'
[ "$(stat -c %u "$target")" = 0 ] && [ "$(stat -c %a "$target")" = 755 ] && [ "$(stat -c %h "$target")" = 1 ] || fail 'the installed launcher is not protected by root'
[ "$(stat -c %s "$target")" = '@LAUNCH_SIZE@' ] || fail 'the installed launcher has the wrong size'
actual=$(dd if="$target" bs=1048576 count='@LAUNCH_SIZE@' iflag=count_bytes,nofollow,nonblock status=none | sha256sum | cut -d ' ' -f 1)
[ "$actual" = '@LAUNCH_SHA256@' ] || fail 'the installed launcher differs from this release'
printf '%s\n' '{"op":"slots"}' | /usr/bin/sudo -n "$target"
