# Included only in the launcher case of undo_step. The sudoers rule is
# removed/restored before the shared executable or accounts are changed.
restore '/etc/sudoers.d/@BRAND@-@USER@' "$j/sudoers" || return 1
m="$base/machine/launcher"
if [ -d "$m" ]; then
    safe "$m"
    safe "$m/users"
    rm -f "$m/users/$u" || return 1
    remaining=$(find "$m/users" -mindepth 1 -maxdepth 1 -type f -printf x) || return 1
    if [ -z "$remaining" ]; then
        restore '/usr/local/libexec/@BRAND@/launch' "$m/launcher" || return 1
        rm -rf "$m" || return 1
    fi
fi
