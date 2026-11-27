if begin kvm-group; then
    getent group kvm >/dev/null || fail 'the kvm group is missing'
    if id -nG '@USER@' | tr ' ' '\n' | awk '$0 == "kvm" { found=1 } END { exit !found }'; then
        : > "$j/unchanged"
    else
        usermod -aG kvm '@USER@'
    fi
    record
fi
