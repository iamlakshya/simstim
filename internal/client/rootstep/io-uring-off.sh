if begin io-uring-off; then
    for p in /etc /etc/sysctl.d; do dir "$p"; done
    path='/etc/sysctl.d/50-@BRAND@.conf'
    machine_begin io-uring-off "$path"
    tmp=$(mktemp '/etc/sysctl.d/.@BRAND@.XXXXXXXX')
    printf 'kernel.io_uring_disabled = 2\n' > "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$path"
    sysctl -p "$path"
    [ "$(sysctl -n kernel.io_uring_disabled)" = 2 ] || fail 'io_uring setting did not change'
    record
fi
