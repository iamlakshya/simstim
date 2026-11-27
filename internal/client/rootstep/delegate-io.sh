if begin delegate-io; then
    for p in /etc /etc/systemd /etc/systemd/system /etc/systemd/system/user@@UID@.service.d; do dir "$p"; done
    path='/etc/systemd/system/user@@UID@.service.d/50-@BRAND@-delegate.conf'
    backup "$path" file
    tmp=$(mktemp '/etc/systemd/system/user@@UID@.service.d/.@BRAND@.XXXXXXXX')
    printf '[Service]\nDelegate=cpu cpuset io memory pids\n' > "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$path"
    systemctl daemon-reload
    record
fi
