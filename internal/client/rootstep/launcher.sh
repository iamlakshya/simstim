if begin launcher; then
    # The trusted client formats both constants from its original release
    # item. Host text and request fields cannot choose this source or hash.
    launch_sha='@LAUNCH_SHA256@'
    launch_size='@LAUNCH_SIZE@'
    source="/var/tmp/@BRAND@-launch-@UID@-$launch_sha"
    for p in /usr /usr/local /usr/local/libexec /usr/local/libexec/@BRAND@ /etc /etc/sudoers.d; do dir "$p"; done
    m="$base/machine/launcher"
    dir "$m"
    dir "$m/users"
    target='/usr/local/libexec/@BRAND@/launch'
    if [ ! -e "$m/launcher.before" ] && [ ! -e "$m/launcher.absent" ]; then
        old_j=$j
        j=$m
        backup "$target" launcher
        j=$old_j
    fi
    backup '/etc/sudoers.d/@BRAND@-@USER@' sudoers
    [ -f "$source" ] && [ ! -L "$source" ] || fail 'the client-selected launcher is not a regular staged file'
    [ "$(stat -c %u "$source")" = "$uid" ] || fail 'the staged launcher belongs to another account'
    [ "$(stat -c %s "$source")" = "$launch_size" ] || fail 'the staged launcher has the wrong size'
    tmp=$(mktemp /usr/local/libexec/@BRAND@/launch.XXXXXXXX)
    # Copy at most the declared size plus one, without following a final
    # symlink or blocking on a substituted FIFO. Check the root-owned copy.
    if ! dd if="$source" of="$tmp" bs=1048576 count="$((launch_size + 1))" iflag=count_bytes,nofollow,nonblock status=none; then rm -f "$tmp"; fail 'cannot capture the staged launcher'; fi
    if [ "$(stat -c %s "$tmp")" != "$launch_size" ] || [ "$(sha256sum "$tmp" | cut -d ' ' -f 1)" != "$launch_sha" ]; then rm -f "$tmp"; fail 'the captured launcher differs from the trusted release'; fi
    chmod 0755 "$tmp"
    mv -f "$tmp" "$target"
    tmp=$(mktemp /etc/sudoers.d/.@BRAND@-@USER@.XXXXXXXX)
    printf '%s\n' '@USER@ ALL=(root) NOPASSWD: /usr/local/libexec/@BRAND@/launch ""' > "$tmp"
    chmod 0440 "$tmp"
    if ! visudo -cf "$tmp"; then rm -f "$tmp"; fail 'the launcher sudoers rule did not pass visudo'; fi
    mv -f "$tmp" '/etc/sudoers.d/@BRAND@-@USER@'
    printf '%s\n' "$uid" > "$m/users/$u"
    chmod 0600 "$m/users/$u"
    extra=',"launcher_sha256":"@LAUNCH_SHA256@"'
    record
    extra=''
fi
