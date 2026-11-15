if begin vsock-modules; then
    for p in /etc /etc/modprobe.d; do dir "$p"; done
    path='/etc/modprobe.d/50-@BRAND@-vsock.conf'
    machine_begin vsock-modules "$path"
    tmp=$(mktemp '/etc/modprobe.d/.@BRAND@.XXXXXXXX')
    for module in vsock_loopback vmw_vsock_virtio_transport vhost_vsock; do
        printf 'blacklist %s\ninstall %s /bin/false\n' "$module" "$module" >> "$tmp"
    done
    chmod 0644 "$tmp"
    mv -f "$tmp" "$path"
    record
fi
